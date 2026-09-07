package source

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/samvad-hq/samvad-news-harvester/internal/httpx"
	"github.com/samvad-hq/samvad-news-harvester/internal/news"
)

// maxIndexDepth bounds how deep a sitemap index chain is followed. Cycles
// are already caught by the visited set; this guards a pathologically deep
// but acyclic chain.
const maxIndexDepth = 4

// maxIndexChildren bounds how many children a single sitemap index may
// list. Depth and cycles are already bounded above; this guards fan-out —
// an index listing thousands of children would mean thousands of
// sequential requests inside one Fetch. 200 is well past anything a news
// publisher's index does.
const maxIndexChildren = 200

// maxURLsPerSitemap bounds how many <url> entries a single sitemap
// document may contribute. Without this, one oversized document delays
// every other source's next crawl: RunOnce crawls sources concurrently
// but is called synchronously from the ticker loop, so a document that
// takes long enough to enrich pushes the start of the next tick for
// everyone. jagran alone lists about 2500 <url> entries today, and
// Google's news sitemap specification caps a single document at 1000, so
// 5000 is headroom past any compliant publisher rather than a limit one
// is expected to hit — it exists to bound a pathological or malicious
// document, not to trim a real one.
const maxURLsPerSitemap = 5000

// Sitemap fetches news sitemaps. It handles both a plain <urlset> and a
// <sitemapindex> whose children are fetched in turn.
type Sitemap struct {
	clients *httpx.ProxyCache
	log     *slog.Logger
}

// NewSitemap returns a fetcher that draws clients from the given cache, so
// a source with a proxy configured egresses through it.
func NewSitemap(clients *httpx.ProxyCache, log *slog.Logger) *Sitemap {
	return &Sitemap{clients: clients, log: log}
}

// Fetch returns the articles listed by the source's sitemap. Articles are
// deduplicated by canonical URL within the document, so a publisher listing
// one story under several spellings yields one article.
func (s *Sitemap) Fetch(ctx context.Context, src Config) ([]news.Article, error) {
	if src.Type != TypeNewsSitemap {
		return nil, fmt.Errorf("source %s: sitemap fetcher cannot handle type %q", src.ID, src.Type)
	}

	client, err := s.clients.For(src.Proxy)
	if err != nil {
		return nil, fmt.Errorf("source %s: %w", src.ID, err)
	}

	entries, err := s.collect(ctx, client, src, src.URL, make(map[string]struct{}), 0)
	if err != nil {
		return nil, err
	}
	return s.build(src, entries), nil
}

// collect walks the sitemap at rawURL, following an index if it finds one.
func (s *Sitemap) collect(
	ctx context.Context,
	client *httpx.Client,
	src Config,
	rawURL string,
	visited map[string]struct{},
	depth int,
) ([]urlEntry, error) {
	if depth > maxIndexDepth {
		s.log.WarnContext(ctx, "sitemap index nested deeper than the limit",
			"source", src.ID, "url", rawURL, "max_depth", maxIndexDepth)
		return nil, nil
	}
	if _, seen := visited[rawURL]; seen {
		return nil, nil
	}
	visited[rawURL] = struct{}{}

	body, err := s.get(ctx, client, src, rawURL)
	if err != nil {
		return nil, err
	}

	// Identify the root element before deciding how to parse. Without this,
	// encoding/xml happily decodes an HTML challenge page or an RSS/Atom
	// feed into a struct with no matching children — indistinguishable from
	// a genuinely empty sitemap, so a blocked or misconfigured source would
	// otherwise report zero articles and no error, forever.
	kind, root, err := rootKind(body)
	if err != nil {
		return nil, fmt.Errorf("source %s: decode %s: %w", src.ID, rawURL, err)
	}

	switch kind {
	case kindURLSet:
		entries, err := parseURLSet(body)
		if err != nil {
			return nil, fmt.Errorf("source %s: decode sitemap %s: %w", src.ID, rawURL, err)
		}
		if len(entries) > maxURLsPerSitemap {
			s.log.WarnContext(ctx, "sitemap document exceeds the per-document url cap and was truncated",
				"source", src.ID, "url", rawURL, "urls", len(entries), "max_urls", maxURLsPerSitemap)
			entries = entries[:maxURLsPerSitemap]
		}
		return entries, nil
	case kindIndex:
		return s.collectIndex(ctx, client, src, rawURL, body, visited, depth)
	default:
		return nil, fmt.Errorf("source %s: %s is not a sitemap: root element is <%s>", src.ID, rawURL, root)
	}
}

// collectIndex fetches every child of a sitemap index. A child that fails
// is logged and skipped rather than aborting the whole index, so one flaky
// or 500-ing child does not cost the siblings that parsed fine. An error is
// returned only when every child failed.
func (s *Sitemap) collectIndex(
	ctx context.Context,
	client *httpx.Client,
	src Config,
	rawURL string,
	body []byte,
	visited map[string]struct{},
	depth int,
) ([]urlEntry, error) {
	children, err := parseSitemapIndex(body)
	if err != nil {
		return nil, fmt.Errorf("source %s: decode sitemap index %s: %w", src.ID, rawURL, err)
	}

	if len(children) > maxIndexChildren {
		s.log.WarnContext(ctx, "sitemap index exceeds the child fan-out limit and was truncated",
			"source", src.ID, "url", rawURL, "children", len(children), "max_children", maxIndexChildren)
		children = children[:maxIndexChildren]
	}

	indexHost := hostname(rawURL)

	var all []urlEntry
	failed := 0
	for _, child := range children {
		// A child on a different host than the index document that named
		// it is not something a legitimate sitemap needs: it is either a
		// publisher mistake or a publisher-controlled document trying to
		// point this crawl at somewhere unrelated. Publishers do
		// sometimes shard onto a CDN subdomain of their own, which is why
		// this compares a loose, registrable-looking suffix rather than
		// requiring an exact host match.
		childHost := hostname(child.Loc)
		if indexHost != "" && childHost != "" && !sameRegistrableHost(indexHost, childHost) {
			s.log.WarnContext(ctx, "skipping sitemap index child on a different host than the index",
				"source", src.ID, "index_host", indexHost, "child_host", childHost, "child", child.Loc)
			failed++
			continue
		}

		nested, err := s.collect(ctx, client, src, child.Loc, visited, depth+1)
		if err != nil {
			s.log.WarnContext(ctx, "skipping sitemap index child that failed to fetch",
				"source", src.ID, "index", rawURL, "child", child.Loc, "error", err)
			failed++
			continue
		}
		all = append(all, nested...)
	}

	if len(children) > 0 && failed == len(children) {
		return nil, fmt.Errorf("source %s: all %d children of sitemap index %s failed", src.ID, failed, rawURL)
	}
	return all, nil
}

// get performs one sitemap request and rejects a non-200 response.
func (s *Sitemap) get(ctx context.Context, client *httpx.Client, src Config, rawURL string) ([]byte, error) {
	resp, err := client.Get(ctx, rawURL, src.Headers.Map())
	if err != nil {
		return nil, fmt.Errorf("source %s: fetch %s: %w", src.ID, rawURL, err)
	}
	if resp.Status != http.StatusOK {
		return nil, fmt.Errorf("source %s: %s returned status %d: %s",
			src.ID, rawURL, resp.Status, snippet(resp.Body))
	}
	if resp.Truncated {
		s.log.WarnContext(ctx, "sitemap body hit the size cap and was truncated",
			"source", src.ID, "url", rawURL)
	}
	return resp.Body, nil
}

// build turns sitemap entries into articles, dropping locations that are
// not usable URLs and collapsing duplicates by canonical form.
func (s *Sitemap) build(src Config, entries []urlEntry) []news.Article {
	out := make([]news.Article, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))

	for _, e := range entries {
		original := strings.TrimSpace(e.Loc)
		if original == "" {
			continue
		}

		canonical, err := news.CanonicalURL(original)
		if err != nil {
			s.log.Debug("skipping unusable sitemap location",
				"source", src.ID, "loc", original, "error", err)
			continue
		}
		if _, dup := seen[canonical]; dup {
			continue
		}
		seen[canonical] = struct{}{}

		article := news.Article{
			SourceID: src.ID,
			ID:       news.ID(canonical),
			Title:    strings.TrimSpace(e.News.Title),
			URL:      canonical,
			ImageURL: firstImage(e.Images),
			Keywords: parseKeywords(e.News.Keywords),
		}
		if original != canonical {
			article.OriginalURL = original
		}
		if published, ok := parsePublicationDate(e.News.PublicationDate); ok {
			article.PublishedAt = published
		} else if published, ok := parsePublicationDate(e.LastMod); ok {
			article.PublishedAt = published
		} else {
			s.log.Debug("sitemap entry has no usable publish date",
				"source", src.ID, "url", canonical,
				"publication_date", e.News.PublicationDate, "lastmod", e.LastMod)
		}
		out = append(out, article)
	}
	return out
}

// snippet trims a response body for an error message.
func snippet(body []byte) string {
	const max = 256
	s := strings.TrimSpace(string(body))
	if s == "" {
		return "<empty>"
	}
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

// hostname returns rawURL's host, or "" if it does not parse into one.
// Used only for the sitemap index host comparison, where an unparseable
// location is already handled elsewhere (it will fail again, with a
// proper error, when actually fetched).
func hostname(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// shortSecondLevelLabels are second-level labels conventionally used
// ahead of a country-code TLD (thehindu.co.in, bbc.co.uk, news.com.au),
// where the registrable-looking suffix is really the last three labels,
// not two.
var shortSecondLevelLabels = map[string]bool{
	"co": true, "com": true, "org": true, "net": true, "gov": true, "ac": true, "edu": true,
}

// registrableSuffix returns a loose approximation of a host's
// registrable domain: normally its last two labels, or its last three
// when the second-to-last label looks like a short second-level label
// ahead of a two- or three-letter country code. It is a heuristic, not a
// public-suffix-list lookup — it will treat some unrelated hosts under an
// uncommon multi-part TLD as matching, and vice versa for some legitimate
// three-plus-label CDN names — but needs no new dependency and is enough
// to catch a sitemap index child on a genuinely unrelated domain while
// still allowing a publisher's own CDN subdomain of its main domain.
func registrableSuffix(host string) string {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return host
	}
	n := 2
	if len(labels) >= 3 {
		secondLast := labels[len(labels)-2]
		lastLabel := labels[len(labels)-1]
		if shortSecondLevelLabels[secondLast] && len(lastLabel) <= 3 {
			n = 3
		}
	}
	return strings.Join(labels[len(labels)-n:], ".")
}

// sameRegistrableHost loosely compares two hosts by registrableSuffix.
func sameRegistrableHost(a, b string) bool {
	return registrableSuffix(a) == registrableSuffix(b)
}
