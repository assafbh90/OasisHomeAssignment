package discover_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/automation/discover"
)

func TestSitemap_Discover_FiltersAndOrders(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>` + base + `/blog/old</loc><lastmod>2024-01-01</lastmod></url>
  <url><loc>` + base + `/blog/new</loc><lastmod>2025-06-01</lastmod></url>
  <url><loc>` + base + `/about</loc><lastmod>2025-06-02</lastmod></url>
</urlset>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	d := discover.New(5 * time.Second)
	urls, err := d.Discover(context.Background(), srv.URL+"/blog")
	require.NoError(t, err)
	// Only /blog/* (not /about), newest lastmod first.
	require.Equal(t, []string{srv.URL + "/blog/new", srv.URL + "/blog/old"}, urls)
}

func TestSitemap_Discover_FollowsIndex(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <sitemap><loc>` + baseOf(r) + `/child.xml</loc></sitemap>
</sitemapindex>`))
	})
	mux.HandleFunc("/child.xml", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>` + baseOf(r) + `/blog/post-1</loc></url>
</urlset>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	d := discover.New(5 * time.Second)
	urls, err := d.Discover(context.Background(), srv.URL)
	require.NoError(t, err)
	require.Equal(t, []string{srv.URL + "/blog/post-1"}, urls)
}

func TestSitemap_Discover_NoSitemap(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	d := discover.New(5 * time.Second)
	_, err := d.Discover(context.Background(), srv.URL)
	require.Error(t, err)
	require.Contains(t, err.Error(), "sitemap")
}

// baseOf returns the test server's scheme://host for building absolute locs.
func baseOf(r *http.Request) string { return "http://" + r.Host }
