package summarize_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/automation/summarize"
)

func TestOllama_Summarize(t *testing.T) {
	t.Parallel()
	var gotModel, gotPrompt, gotFormat string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/generate", r.URL.Path)
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Model  string `json:"model"`
			Prompt string `json:"prompt"`
			Stream bool   `json:"stream"`
			Format string `json:"format"`
		}
		_ = json.Unmarshal(body, &req)
		gotModel, gotPrompt, gotFormat = req.Model, req.Prompt, req.Format
		require.False(t, req.Stream)
		// The model emits a JSON object in the "response" field.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"response": `{"title":"Clean Title","source":"Oasis Security","type":"guide","summary":"A concise summary."}`,
		})
	}))
	defer srv.Close()

	o := summarize.New(srv.URL, "qwen2.5:0.5b", 10*time.Second, 50)
	out, err := o.Summarize(context.Background(), "Page Title", strings.Repeat("x", 200))
	require.NoError(t, err)
	require.Equal(t, "Clean Title", out.Title)
	require.Equal(t, "Oasis Security", out.Source)
	require.Equal(t, "guide", out.Type)
	require.Equal(t, "A concise summary.", out.Body)
	require.Equal(t, "qwen2.5:0.5b", gotModel)
	require.Equal(t, "json", gotFormat)
	require.Contains(t, gotPrompt, "Page Title")               // page title is included
	require.NotContains(t, gotPrompt, strings.Repeat("x", 60)) // input truncated to ~maxInput
}

func TestOllama_Summarize_FallbackOnNonJSON(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"response": "  just plain prose  "})
	}))
	defer srv.Close()

	o := summarize.New(srv.URL, "m", 10*time.Second, 0)
	out, err := o.Summarize(context.Background(), "Page Title", "body")
	require.NoError(t, err)
	require.Equal(t, "Page Title", out.Title) // falls back to the page title
	require.Equal(t, "just plain prose", out.Body)
}
