// Package discover finds candidate post URLs for an automation by reading the
// site's sitemap.xml (sitemap-index aware). It is intentionally the only
// discovery strategy: sites without a sitemap are reported as an error.
package discover

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/samber/lo"

	"github.com/assafbh/identityhub/internal/httpconst"
)

const (
	// sitemapPath is the conventional sitemap location, fetched relative to the site root.
	sitemapPath = "/sitemap.xml"
	// maxSitemapBytes caps a single sitemap document read (defensive against huge files).
	maxSitemapBytes = 10 << 20 // 10 MiB
)

// Sitemap discovers URLs via sitemap.xml.
type Sitemap struct {
	http *http.Client
}

// New constructs the discoverer with a per-request timeout.
func New(timeout time.Duration) *Sitemap {
	return &Sitemap{http: &http.Client{Timeout: timeout}}
}

type sitemapDoc struct {
	URLs []struct {
		Loc     string `xml:"loc"`
		LastMod string `xml:"lastmod"`
	} `xml:"url"`
	Sitemaps []struct {
		Loc string `xml:"loc"`
	} `xml:"sitemap"`
}

// Discover returns post URLs under siteURL (prefix match), newest lastmod first.
func (s *Sitemap) Discover(ctx context.Context, siteURL string) ([]string, error) {
	base, err := url.Parse(siteURL)
	if err != nil {
		return nil, fmt.Errorf("parse site url: %w", err)
	}
	root := base.Scheme + "://" + base.Host
	prefix := strings.TrimRight(siteURL, "/")

	doc, err := s.fetch(ctx, root+sitemapPath)
	if err != nil {
		return nil, fmt.Errorf("no sitemap.xml found at %s: %w", root, err)
	}

	type entry struct {
		loc     string
		lastmod string
	}
	var entries []entry

	if len(doc.Sitemaps) > 0 {
		// Sitemap index: follow one level of children.
		for _, sm := range doc.Sitemaps {
			child, err := s.fetch(ctx, sm.Loc)
			if err != nil {
				continue // skip an unreachable child sitemap
			}
			for _, urlNode := range child.URLs {
				entries = append(entries, entry{loc: urlNode.Loc, lastmod: urlNode.LastMod})
			}
		}
	} else {
		for _, urlNode := range doc.URLs {
			entries = append(entries, entry{loc: urlNode.Loc, lastmod: urlNode.LastMod})
		}
	}

	// Keep only URLs strictly under the watched prefix.
	posts := lo.Filter(entries, func(candidate entry, _ int) bool {
		loc := strings.TrimSpace(candidate.loc)
		return strings.HasPrefix(loc, prefix) && len(loc) > len(prefix)
	})
	if len(posts) == 0 {
		return nil, fmt.Errorf("no posts under %s in sitemap", prefix)
	}

	// Newest lastmod first; entries without a lastmod sort last (stable).
	slices.SortStableFunc(posts, func(left, right entry) int {
		return strings.Compare(right.lastmod, left.lastmod)
	})
	return lo.Map(posts, func(candidate entry, _ int) string { return strings.TrimSpace(candidate.loc) }), nil
}

func (s *Sitemap) fetch(ctx context.Context, sitemapURL string) (*sitemapDoc, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sitemapURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if !httpconst.IsSuccessStatus(resp.StatusCode) {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxSitemapBytes))
	if err != nil {
		return nil, err
	}
	var doc sitemapDoc
	if err := xml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse sitemap xml: %w", err)
	}
	return &doc, nil
}
