package crawler

import (
	"bytes"
	"context"
	"testing"

	"github.com/samvad-hq/samvad-news-harvester/internal/domain"
	"github.com/samvad-hq/samvad-news-harvester/pkg/httpclient"
	"github.com/samvad-hq/samvad-news-harvester/pkg/providers"
)

// stubHTTPResponse implements httpclient.Response.
type stubHTTPResponse struct {
	body       []byte
	statusCode int
}

func (s stubHTTPResponse) Body() []byte    { return s.body }
func (s stubHTTPResponse) StatusCode() int { return s.statusCode }

// stubHTTPClient returns a single response.
type stubHTTPClient struct {
	resp httpclient.Response
}

func (s stubHTTPClient) Get(_ context.Context, _ string, _ map[string]string) (httpclient.Response, error) {
	return s.resp, nil
}

func TestParseMetaPrefersOGTags(t *testing.T) {
	html := []byte(`
<html>
  <head>
    <title>Fallback</title>
    <meta property="og:title" content="OG Title">
    <meta property="og:description" content="OG Desc">
    <meta property="og:image" content="/img/og.png">
  </head>
</html>`)

	meta, err := parseMeta(html)
	if err != nil {
		t.Fatalf("parseMeta: %v", err)
	}
	if meta.Title != "OG Title" || meta.Description != "OG Desc" || meta.ImageURL != "/img/og.png" {
		t.Fatalf("unexpected meta %#v", meta)
	}
}

func TestResolveURLHandlesRelative(t *testing.T) {
	got := resolveURL("/img.png", "https://example.com/articles/1")
	if got != "https://example.com/img.png" {
		t.Fatalf("resolveURL got %q", got)
	}

	if got := resolveURL("", "https://example.com"); got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}

func TestScraperEnrichesAndLimitsBody(t *testing.T) {
	body := bytes.Repeat([]byte("a"), maxHTMLBodyBytes+10)
	resp := stubHTTPResponse{body: body, statusCode: 200}

	scraper := NewScraper(stubHTTPClient{resp: resp}, nil)
	cfg := providers.Provider{ID: "p1", RequestDelayMs: 1}
	articles := []domain.Article{{ID: "a1", URL: "https://example.com"}}

	enriched := scraper.Enrich(context.Background(), cfg, articles)
	if len(enriched) != 1 {
		t.Fatalf("expected 1 article")
	}
	if len(enriched[0].Title) != 0 {
		t.Fatalf("expected empty title because body had no metadata")
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", " ", "foo", "bar"); got != "foo" {
		t.Fatalf("firstNonEmpty returned %q", got)
	}
}
