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

Return ONLY a single JSON object with exactly these string fields:
  "title":   the article headline, cleaned of any site name or section suffix
  "source":  the publication or website name (e.g. "Oasis Security")
  "type":    the content type in one lowercase word (e.g. guide, blog, article, news)
  "summary": a thorough summary (see rules)

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

// modelSummary is the JSON shape the model is asked to emit (in Response).
type modelSummary struct {
	Title   string `json:"title"`
	Source  string `json:"source"`
	Type    string `json:"type"`
	Summary string `json:"summary"`
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
		Format: "json",
	})
	if err != nil {
		return domain.PostSummary{}, fmt.Errorf("marshal ollama request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/api/generate", bytes.NewReader(reqBody))
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

// parseSummary turns the model's JSON response into a PostSummary. If the model
// did not produce parseable JSON (small models occasionally don't), it falls back
// to treating the whole response as the summary body with the page title.
func parseSummary(response, pageTitle string) (domain.PostSummary, error) {
	raw := strings.TrimSpace(response)
	var m modelSummary
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		if raw == "" {
			return domain.PostSummary{}, fmt.Errorf("ollama returned an empty summary")
		}
		return domain.PostSummary{Title: pageTitle, Body: raw}, nil
	}

	body := strings.TrimSpace(m.Summary)
	if body == "" {
		return domain.PostSummary{}, fmt.Errorf("ollama returned an empty summary")
	}
	title := strings.TrimSpace(m.Title)
	if title == "" {
		title = pageTitle
	}
	return domain.PostSummary{
		Title:  title,
		Source: strings.TrimSpace(m.Source),
		Type:   strings.TrimSpace(m.Type),
		Body:   body,
	}, nil
}
