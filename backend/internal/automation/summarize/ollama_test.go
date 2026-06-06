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
	var gotModel, gotPrompt string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/generate", r.URL.Path)
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Model  string `json:"model"`
			Prompt string `json:"prompt"`
			Stream bool   `json:"stream"`
		}
		_ = json.Unmarshal(body, &req)
		gotModel, gotPrompt = req.Model, req.Prompt
		require.False(t, req.Stream)
		_ = json.NewEncoder(w).Encode(map[string]any{"response": "  A concise summary. "})
	}))
	defer srv.Close()

	o := summarize.New(srv.URL, "qwen2.5:0.5b", 10*time.Second, 20)
	out, err := o.Summarize(context.Background(), strings.Repeat("x", 100))
	require.NoError(t, err)
	require.Equal(t, "A concise summary.", out)
	require.Equal(t, "qwen2.5:0.5b", gotModel)
	require.LessOrEqual(t, len(gotPrompt), 20+200) // input truncated to ~maxInput + fixed instruction
}
