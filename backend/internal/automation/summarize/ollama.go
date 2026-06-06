// Package summarize turns post markdown into a short summary via a local Ollama
// model. The HTTP call is non-streaming and the input is truncated to bound
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

const promptTemplate = "Summarize the following blog post in 3-5 sentences for a Jira ticket. " +
	"Be factual and concise.\n\n---\n%s\n---\nSummary:"

type generateRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Stream bool   `json:"stream"`
}

type generateResponse struct {
	Response string `json:"response"`
}

// Summarize returns a short summary of the given markdown.
func (o *Ollama) Summarize(ctx context.Context, markdown string) (string, error) {
	in := markdown
	if o.maxInput > 0 && len(in) > o.maxInput {
		in = in[:o.maxInput]
	}
	reqBody, err := json.Marshal(generateRequest{
		Model:  o.model,
		Prompt: fmt.Sprintf(promptTemplate, in),
		Stream: false,
	})
	if err != nil {
		return "", fmt.Errorf("marshal ollama request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/api/generate", bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("ollama request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("ollama returned %d", resp.StatusCode)
	}
	var out generateResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode ollama response: %w", err)
	}
	summary := strings.TrimSpace(out.Response)
	if summary == "" {
		return "", fmt.Errorf("ollama returned an empty summary")
	}
	return summary, nil
}
