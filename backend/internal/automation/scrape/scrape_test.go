package scrape_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/automation/scrape"
)

func TestScraper_Scrape_TitleAndMarkdown(t *testing.T) {
	t.Parallel()
	page := `<!DOCTYPE html><html><head><title>My Post Title</title></head>
<body>
  <nav>menu noise that should be stripped</nav>
  <article>
    <h1>My Post Title</h1>
    <p>This is the first paragraph of the article body with enough text to be
    considered the main content by the readability extractor, repeated to add
    length. This is the first paragraph of the article body with enough text.</p>
    <p>A second meaningful paragraph follows with more words so extraction works.</p>
  </article>
</body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()

	s := scrape.New(5 * time.Second)
	title, md, err := s.Scrape(context.Background(), srv.URL+"/blog/post")
	require.NoError(t, err)
	require.Equal(t, "My Post Title", title)
	require.Contains(t, md, "first paragraph")
	require.NotContains(t, strings.ToLower(md), "menu noise")
}

func TestScraper_Scrape_Non2xx(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	s := scrape.New(5 * time.Second)
	_, _, err := s.Scrape(context.Background(), srv.URL)
	require.Error(t, err)
}
