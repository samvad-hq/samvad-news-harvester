package enrich_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/samvad-hq/samvad-news-harvester/internal/enrich"
	"github.com/samvad-hq/samvad-news-harvester/internal/httpx"
	"github.com/samvad-hq/samvad-news-harvester/internal/news"
	"github.com/samvad-hq/samvad-news-harvester/internal/source"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

func newScraper(t *testing.T) *enrich.Scraper {
	t.Helper()
	// httptest servers are on loopback, so tests in this file must opt out
	// of the default SSRF guard to reach them.
	return enrich.NewScraper(
		httpx.NewProxyCache(5*time.Second, 1<<20, httpx.WithPrivateNetworksAllowed()),
		httpx.NewHostLimiter(rate.Limit(1000), 100),
		4,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
}

func testSource() source.Config {
	return source.Config{
		ID:      "test",
		Name:    "Test",
		Type:    source.TypeNewsSitemap,
		Headers: source.Headers{UserAgent: "test-agent"},
	}
}

func TestEnrichFillsMetadataFromOpenGraph(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<html><head>
			<title>Fallback Title</title>
			<meta property="og:title" content="OG Title">
			<meta property="og:description" content="OG Description">
			<meta property="og:image" content="/images/hero.jpg">
		</head><body></body></html>`)
	}))
	defer srv.Close()

	in := []news.Article{{ID: "a", URL: srv.URL + "/story", Title: "Sitemap Title"}}
	got := newScraper(t).Enrich(context.Background(), testSource(), in)

	require.Len(t, got, 1)
	require.Equal(t, "OG Title", got[0].Title)
	require.Equal(t, "OG Description", got[0].Description)
	require.Equal(t, srv.URL+"/images/hero.jpg", got[0].ImageURL, "relative image URLs resolve against the article")
}

func TestEnrichFallsBackToTitleAndMetaDescription(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<html><head>
			<title>  Plain Title  </title>
			<meta name="description" content="Plain description">
		</head></html>`)
	}))
	defer srv.Close()

	in := []news.Article{{ID: "a", URL: srv.URL + "/story"}}
	got := newScraper(t).Enrich(context.Background(), testSource(), in)

	require.Equal(t, "Plain Title", got[0].Title)
	require.Equal(t, "Plain description", got[0].Description)
}

func TestEnrichKeepsSitemapValuesWhenThePageOffersNothing(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<html><head></head><body>no metadata</body></html>`)
	}))
	defer srv.Close()

	in := []news.Article{{
		ID:       "a",
		URL:      srv.URL + "/story",
		Title:    "Sitemap Title",
		ImageURL: "https://cdn.example/from-sitemap.jpg",
	}}
	got := newScraper(t).Enrich(context.Background(), testSource(), in)

	require.Equal(t, "Sitemap Title", got[0].Title)
	require.Equal(t, "https://cdn.example/from-sitemap.jpg", got[0].ImageURL)
}

// TestEnrichKeepsTheSitemapImageWhenOGImageIsUnparseable pins finding #7:
// an unparseable og:image must not overwrite a valid sitemap ImageURL
// with junk.
func TestEnrichKeepsTheSitemapImageWhenOGImageIsUnparseable(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<html><head>
			<meta property="og:image" content="/img/%zz.jpg">
		</head></html>`)
	}))
	defer srv.Close()

	in := []news.Article{{
		ID:       "a",
		URL:      srv.URL + "/story",
		ImageURL: "https://cdn.example/from-sitemap.jpg",
	}}
	got := newScraper(t).Enrich(context.Background(), testSource(), in)

	require.Equal(t, "https://cdn.example/from-sitemap.jpg", got[0].ImageURL,
		"an unparseable og:image must not overwrite a good sitemap image")
}

func TestEnrichKeepsTheArticleWhenThePageFails(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	in := []news.Article{{ID: "a", URL: srv.URL + "/story", Title: "Sitemap Title"}}
	got := newScraper(t).Enrich(context.Background(), testSource(), in)

	require.Len(t, got, 1, "a failed page must not drop the article")
	require.Equal(t, "Sitemap Title", got[0].Title)
}

func TestEnrichPreservesOrder(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `<html><head><meta property="og:description" content="d`+r.URL.Path+`"></head></html>`)
	}))
	defer srv.Close()

	in := make([]news.Article, 20)
	for i := range in {
		in[i] = news.Article{ID: string(rune('a' + i)), URL: srv.URL + "/" + string(rune('a'+i))}
	}

	got := newScraper(t).Enrich(context.Background(), testSource(), in)
	require.Len(t, got, 20)
	for i := range got {
		require.Equal(t, in[i].ID, got[i].ID, "position %d", i)
	}
}

func TestEnrichReturnsPromptlyOnCancellation(t *testing.T) {
	t.Parallel()

	var hits atomic.Int64
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		<-block
	}))
	defer func() { close(block); srv.Close() }()

	in := make([]news.Article, 50)
	for i := range in {
		in[i] = news.Article{ID: "x", URL: srv.URL + "/story", Title: "Kept"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan []news.Article, 1)
	go func() { done <- newScraper(t).Enrich(ctx, testSource(), in) }()

	select {
	case got := <-done:
		require.Len(t, got, 50, "cancellation returns the originals, not a short slice")
		require.Equal(t, "Kept", got[0].Title)
	case <-time.After(5 * time.Second):
		t.Fatal("Enrich did not return after its context was cancelled")
	}
}

func TestEnrichHandlesAnEmptyBatch(t *testing.T) {
	t.Parallel()

	got := newScraper(t).Enrich(context.Background(), testSource(), nil)
	require.Empty(t, got)
}
