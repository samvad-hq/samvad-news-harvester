// Package enrich fills in the article metadata a news sitemap does not
// carry. A <url> record has a title and a publication date but no
// description, so the only way to obtain one is to fetch the page and read
// its Open Graph tags.
//
// Enrichment is best effort by design: a page that will not load leaves
// its article with the title the sitemap already provided, which is still
// a usable event.
package enrich

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/samvad-hq/samvad-news-harvester/internal/httpx"
	"github.com/samvad-hq/samvad-news-harvester/internal/news"
	"github.com/samvad-hq/samvad-news-harvester/internal/source"
	"golang.org/x/sync/errgroup"
)

// Scraper reads Open Graph metadata from article pages.
type Scraper struct {
	clients     *httpx.ProxyCache
	limiter     *httpx.HostLimiter
	concurrency int
	log         *slog.Logger
}

// NewScraper returns a scraper that fetches at most concurrency pages at
// once, subject to the shared per-host limiter.
func NewScraper(clients *httpx.ProxyCache, limiter *httpx.HostLimiter, concurrency int, log *slog.Logger) *Scraper {
	if concurrency < 1 {
		concurrency = 1
	}
	return &Scraper{clients: clients, limiter: limiter, concurrency: concurrency, log: log}
}

// Enrich returns arts with metadata filled in where a page provided it.
// The result always has the same length and order as the input: an article
// whose page could not be read is returned unchanged.
//
// It takes no error because there is no failure mode worth propagating —
// the caller would publish the articles either way.
func (s *Scraper) Enrich(ctx context.Context, src source.Config, arts []news.Article) []news.Article {
	out := make([]news.Article, len(arts))
	copy(out, arts)
	if len(arts) == 0 {
		return out
	}

	client, err := s.clients.For(src.Proxy)
	if err != nil {
		s.log.ErrorContext(ctx, "cannot build a client for source, skipping enrichment",
			"source", src.ID, "error", err)
		return out
	}

	headers := src.Headers.Map()

	// errgroup with a limit replaces the old channel pool. The old producer
	// checked ctx.Err() and then blocked on an unbuffered send, so a
	// cancellation landing between the two left every worker gone and the
	// producer blocked forever.
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(s.concurrency)

	for i := range arts {
		if gctx.Err() != nil {
			break
		}
		g.Go(func() error {
			// out[i] is pre-populated with the original, so a skip is safe.
			// Each goroutine owns exactly one index, so there is no race.
			if enriched, ok := s.fetchOne(gctx, client, src, headers, arts[i]); ok {
				out[i] = enriched
			}
			return nil
		})
	}
	// The goroutines never return an error, so Wait cannot fail.
	_ = g.Wait()

	return out
}

// fetchOne reads one article page. It reports false when the article
// should be left as it was.
func (s *Scraper) fetchOne(
	ctx context.Context,
	client *httpx.Client,
	src source.Config,
	headers map[string]string,
	art news.Article,
) (news.Article, bool) {
	if err := s.limiter.Wait(ctx, art.URL); err != nil {
		return art, false // context is done
	}

	resp, err := client.Get(ctx, art.URL, headers)
	if err != nil {
		if ctx.Err() == nil {
			s.log.WarnContext(ctx, "article metadata fetch failed",
				"source", src.ID, "url", art.URL, "error", err)
		}
		return art, false
	}
	if resp.Status != http.StatusOK {
		s.log.WarnContext(ctx, "article metadata fetch returned a non-200 status",
			"source", src.ID, "url", art.URL, "status", resp.Status)
		return art, false
	}

	meta, err := parseMeta(resp.Body)
	if err != nil {
		s.log.WarnContext(ctx, "article page could not be parsed",
			"source", src.ID, "url", art.URL, "error", err)
		return art, false
	}

	return apply(art, meta), true
}

// pageMeta is what an article page can contribute.
type pageMeta struct {
	Title       string
	Description string
	ImageURL    string
}

// apply overlays page metadata onto an article, keeping the sitemap's
// value wherever the page offered nothing.
func apply(art news.Article, meta pageMeta) news.Article {
	if meta.Title != "" {
		art.Title = meta.Title
	}
	if meta.Description != "" {
		art.Description = meta.Description
	}
	if meta.ImageURL != "" {
		// resolveURL returns "" when it cannot be resolved, so a bad
		// og:image never overwrites a valid sitemap ImageURL with a
		// meaningless value — leaving art.ImageURL untouched is exactly
		// what happens when the page offers no image at all.
		if resolved := resolveURL(meta.ImageURL, art.URL); resolved != "" {
			art.ImageURL = resolved
		}
	}
	return art
}

// parseMeta extracts Open Graph and standard metadata from an HTML page.
func parseMeta(body []byte) (pageMeta, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return pageMeta{}, err
	}

	content := func(selector string) string {
		node := doc.Find(selector).First()
		if node.Length() == 0 {
			return ""
		}
		value, ok := node.Attr("content")
		if !ok {
			return ""
		}
		return strings.TrimSpace(value)
	}

	return pageMeta{
		Title: firstNonEmpty(
			content(`meta[property="og:title"]`),
			content(`meta[name="twitter:title"]`),
			strings.TrimSpace(doc.Find("title").First().Text()),
		),
		Description: firstNonEmpty(
			content(`meta[property="og:description"]`),
			content(`meta[name="twitter:description"]`),
			content(`meta[name="description"]`),
		),
		ImageURL: firstNonEmpty(
			content(`meta[property="og:image"]`),
			content(`meta[name="twitter:image"]`),
		),
	}, nil
}

// firstNonEmpty returns the first value that is not blank.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// resolveURL resolves a possibly relative URL against the article's URL.
// It returns "" when either URL fails to parse, rather than the raw,
// unresolved string: apply only overwrites the sitemap's ImageURL when
// this returns something non-empty, so a page's unparseable og:image
// leaves the sitemap's value in place instead of replacing it with junk.
func resolveURL(raw, base string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if parsed.IsAbs() {
		return parsed.String()
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		return ""
	}
	return baseURL.ResolveReference(parsed).String()
}
