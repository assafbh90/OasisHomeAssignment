// Package scrape fetches a post URL and returns its title and a markdown render
// of the main content (boilerplate stripped via readability). No headless
// browser: JS-rendered pages are out of scope.
package scrape

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	readability "github.com/go-shiori/go-readability"

	"github.com/assafbh/identityhub/internal/httpconst"
)

// Scraper fetches and converts post pages.
type Scraper struct {
	http *http.Client
}

// New constructs the scraper with a per-request timeout.
func New(timeout time.Duration) *Scraper {
	return &Scraper{http: &http.Client{Timeout: timeout}}
}

const userAgent = "IdentityHub-Automation/1.0 (+https://github.com/assafbh/identityhub)"

// Scrape returns the post title and markdown body for pageURL.
func (s *Scraper) Scrape(ctx context.Context, pageURL string) (title string, markdown string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := s.http.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("fetch post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if !httpconst.IsSuccessStatus(resp.StatusCode) {
		return "", "", fmt.Errorf("fetch post %s: status %d", pageURL, resp.StatusCode)
	}

	parsed, _ := url.Parse(pageURL)
	article, err := readability.FromReader(resp.Body, parsed)
	if err != nil {
		return "", "", fmt.Errorf("extract article: %w", err)
	}

	md, err := htmltomarkdown.ConvertString(article.Content)
	if err != nil {
		return "", "", fmt.Errorf("convert markdown: %w", err)
	}
	return article.Title, md, nil
}
