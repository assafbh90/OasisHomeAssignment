// Package summarize turns a post's title + markdown into a structured summary via
// a local Ollama model. The HTTP call is non-streaming, the model is asked for
// JSON (so the result is reliably parseable), and the input is truncated to bound
// latency and memory for small models.
package summarize

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/assafbh/identityhub/internal/domain"
)

const (
	// pathGenerate is the Ollama text-generation endpoint.
	pathGenerate = "/api/generate"
	// formatJSON constrains the model to emit a JSON object (Ollama structured output).
	formatJSON = "json"
)

// Ollama is a thin client for the Ollama /api/generate endpoint.
type Ollama struct {
	baseURL  string
	model    string
	http     *http.Client
	maxInput int
}

// New constructs the client. maxInput caps the markdown characters sent.
func New(baseURL, model string, timeout time.Duration, maxInput int) *Ollama {
	return &Ollama{
		baseURL:  strings.TrimRight(baseURL, "/"),
		model:    model,
		http:     &http.Client{Timeout: timeout},
		maxInput: maxInput,
	}
}

// promptTemplate asks for a strict JSON object so the result is parseable, and
// pins down voice (third person, no model self-reference, no "In this blog post"
// preamble), depth (a thorough multi-paragraph summary), and Jira-friendly
// Markdown (**bold** key terms, a short takeaways list) which the client renders
// to ADF. %s is the page title; %s is the (truncated) content.
const promptTemplate = `You extract metadata and write a detailed summary of an article for a Jira ticket.

Return ONLY a single JSON object whose values are ALL plain strings (never nested
objects or arrays), with exactly these fields:
  "title":   the article headline, cleaned of any site name or section suffix
  "source":  the publication or website name (e.g. "Oasis Security")
  "type":    the content type in one lowercase word (e.g. guide, blog, article, news)
  "summary": a thorough summary as a single string (see rules)

Rules for "summary":
  - Cover the article in depth: an opening paragraph with the main thesis, then the
    key points and any concrete recommendations or takeaways. Aim for 8-12 sentences.
  - Use Jira-friendly Markdown: separate paragraphs with a blank line, **bold** the
    most important terms, and you MAY end with a short "- " bulleted list of the key
    takeaways. Do not use headings.
  - Write in the third person about the article's content.
  - Never refer to yourself, an AI, a model, or "the author"; do not use the first person.
  - Do not begin with "In this blog post" or similar; state the substance directly.
  - Be factual; do not invent details not present in the content.

PAGE TITLE: %s

CONTENT:
---
%s
---`

type generateRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Stream bool   `json:"stream"`
	Format string `json:"format,omitempty"` // "json" => the model must emit a JSON object
}

type generateResponse struct {
	Response string `json:"response"`
}

// modelSummary is the JSON shape the model is asked to emit (in Response). Fields
// are RawMessage, not string: small models sometimes emit a nested object/array
// for a field (e.g. echoing the prompt as a "summary" object). jsonText coerces
// each field and ignores anything that isn't a plain string, so a misbehaving
// field never leaks raw JSON into a ticket.
type modelSummary struct {
	Title   json.RawMessage `json:"title"`
	Source  json.RawMessage `json:"source"`
	Type    json.RawMessage `json:"type"`
	Summary json.RawMessage `json:"summary"`
}

// jsonText returns the trimmed string value of raw if it is a JSON string, and
// "" for anything else (object, array, number, null, or absent).
func jsonText(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

// Summarize returns a structured summary of the post. pageTitle is the scraped
// <title>; it both seeds the model's source/type/title extraction and is the
// fallback if the model omits a clean title.
func (o *Ollama) Summarize(ctx context.Context, pageTitle, markdown string) (domain.PostSummary, error) {
	in := markdown
	if o.maxInput > 0 && len(in) > o.maxInput {
		in = in[:o.maxInput]
	}
	reqBody, err := json.Marshal(generateRequest{
		Model:  o.model,
		Prompt: fmt.Sprintf(promptTemplate, pageTitle, in),
		Stream: false,
		Format: formatJSON,
	})
	if err != nil {
		return domain.PostSummary{}, fmt.Errorf("marshal ollama request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+pathGenerate, bytes.NewReader(reqBody))
	if err != nil {
		return domain.PostSummary{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.http.Do(req)
	if err != nil {
		return domain.PostSummary{}, fmt.Errorf("ollama request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return domain.PostSummary{}, fmt.Errorf("ollama returned %d", resp.StatusCode)
	}
	var out generateResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return domain.PostSummary{}, fmt.Errorf("decode ollama response: %w", err)
	}

	return parseSummary(out.Response, pageTitle)
}

// parseSummary turns the model's JSON response into a PostSummary. A usable
// summary string is required: if the model returned no parseable JSON, or emitted
// "summary" as a non-string (a nested object/array — which some small models do
// intermittently), it returns an error so the caller skips the post and retries
// next run, rather than filing a ticket full of raw JSON.
func parseSummary(response, pageTitle string) (domain.PostSummary, error) {
	raw := strings.TrimSpace(response)
	if raw == "" {
		return domain.PostSummary{}, fmt.Errorf("ollama returned an empty response")
	}

	var m modelSummary
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return domain.PostSummary{}, fmt.Errorf("ollama response was not valid JSON: %w", err)
	}

	body := jsonText(m.Summary)
	if body == "" {
		return domain.PostSummary{}, fmt.Errorf("ollama summary was missing or not a string")
	}
	title := jsonText(m.Title)
	if title == "" {
		title = pageTitle
	}
	return domain.PostSummary{
		Title:  title,
		Source: jsonText(m.Source),
		Type:   jsonText(m.Type),
		Body:   body,
	}, nil
}
