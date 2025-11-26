package crawler

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Adda-Baaj/taja-khobor/internal/domain"
	"github.com/Adda-Baaj/taja-khobor/internal/logger"
	"github.com/Adda-Baaj/taja-khobor/pkg/httpclient"
	"github.com/Adda-Baaj/taja-khobor/pkg/providers"

	"github.com/PuerkitoBio/goquery"
)

const (
	maxHTMLBodyBytes  = 1 << 20 // 1 MiB
	maxArticleWorkers = 10
)

// Scraper fetches and enriches article metadata by scraping HTML pages.
type Scraper struct {
	client httpclient.Client
	log    logger.Logger
}

// NewScraper creates a new Scraper with the given HTTP client and logger.
func NewScraper(client httpclient.Client, log logger.Logger) *Scraper {
	if client == nil {
		client = providers.DefaultHTTPClient()
	}
	if log == nil {
		log = logger.NopLogger{}
	}
	return &Scraper{client: client, log: log}
}

// Enrich enriches the given articles by scraping their HTML pages for metadata.
func (s *Scraper) Enrich(ctx context.Context, cfg providers.Provider, articles []domain.Article) []domain.Article {
	delay := cfg.RequestDelay()
	out := make([]domain.Article, len(articles))
	copy(out, articles) // default to originals so partial results are returned on cancel

	if len(articles) == 0 {
		return out
	}

	workerCount := min(len(articles), maxArticleWorkers)

	var limiter <-chan time.Time
	var ticker *time.Ticker
	if delay > 0 {
		ticker = time.NewTicker(delay)
		limiter = ticker.C
		defer ticker.Stop()
	}

	jobCh := make(chan int)
	var wg sync.WaitGroup

	for workerID := range workerCount {
		wg.Add(1)
		go s.articleWorker(ctx, cfg, articles, limiter, jobCh, out, &wg, workerID)
	}

	for idx := range articles {
		if ctx.Err() != nil {
			break
		}
		jobCh <- idx
	}
	close(jobCh)

	wg.Wait()

	return out
}

// articleWorker processes articles from the job channel, respecting the rate limiter, and enriches them by scraping metadata.
func (s *Scraper) articleWorker(
	ctx context.Context,
	cfg providers.Provider,
	articles []domain.Article,
	limiter <-chan time.Time,
	jobCh <-chan int,
	out []domain.Article,
	wg *sync.WaitGroup,
	workerID int,
) {
	defer wg.Done()

	for idx := range jobCh {
		if ctx.Err() != nil {
			return
		}

		if limiter != nil {
			select {
			case <-ctx.Done():
				return
			case <-limiter:
			}
		}

		art := articles[idx]
		if enriched, err := s.fetchAndParse(ctx, cfg, art, workerID); err != nil {
			s.log.WarnObj("article metadata scrape failed", "metadata_error", map[string]any{
				"worker_id":   workerID,
				"provider_id": cfg.ID,
				"url":         art.URL,
				"error":       err.Error(),
			})
			out[idx] = art
		} else {
			out[idx] = enriched
		}
	}
}

// fetchAndParse fetches the article HTML and parses metadata to enrich the article.
func (s *Scraper) fetchAndParse(ctx context.Context, cfg providers.Provider, art domain.Article, workerID int) (domain.Article, error) {
	headers := providers.Headers(cfg)

	s.log.DebugObj("scraping article metadata", "scrape_start", map[string]any{
		"worker_id":   workerID,
		"provider_id": cfg.ID,
		"url":         art.URL,
	})

	resp, err := s.client.Get(ctx, art.URL, headers)
	if err != nil {
		return art, fmt.Errorf("http fetch: %w", err)
	}

	if resp.StatusCode() != 200 {
		snippet := strings.TrimSpace(string(resp.Body()))
		if len(snippet) > 1024 {
			snippet = snippet[:1024]
		}
		return art, fmt.Errorf("status %d body: %s", resp.StatusCode(), snippet)
	}

	body := resp.Body()
	if len(body) > maxHTMLBodyBytes {
		s.log.InfoObj("html body truncated", "truncation", map[string]any{
			"worker_id":   workerID,
			"provider_id": cfg.ID,
			"url":         art.URL,
			"original":    len(body),
			"kept":        maxHTMLBodyBytes,
		})
		body = body[:maxHTMLBodyBytes]
	}

	meta, err := parseMeta(body)
	if err != nil {
		return art, err
	}
	updated := art
	if meta.Title != "" {
		updated.Title = meta.Title
	}
	if meta.Description != "" {
		updated.Description = meta.Description
	}
	if meta.ImageURL != "" {
		updated.ImageURL = resolveURL(meta.ImageURL, art.URL)
	}

	return updated, nil
}

// parseMeta extracts page metadata from the HTML body.
func parseMeta(body []byte) (pageMeta, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return pageMeta{}, fmt.Errorf("parse html: %w", err)
	}

	pm := pageMeta{}

	extract := func(sel string) string {
		if node := doc.Find(sel).First(); node.Length() > 0 {
			if val, ok := node.Attr("content"); ok {
				return strings.TrimSpace(val)
			}
		}
		return ""
	}

	pm.Title = firstNonEmpty(
		extract(`meta[property="og:title"]`),
		strings.TrimSpace(doc.Find("title").First().Text()),
	)
	pm.Description = firstNonEmpty(
		extract(`meta[property="og:description"]`),
		extract(`meta[name="description"]`),
	)
	pm.ImageURL = extract(`meta[property="og:image"]`)

	return pm, nil
}

// pageMeta holds metadata extracted from an HTML page.
type pageMeta struct {
	Title       string
	Description string
	ImageURL    string
}

// firstNonEmpty returns the first non-empty string from the given values.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// resolveURL resolves a possibly relative URL against a base URL.
func resolveURL(raw, base string) string {
	if raw == "" {
		return ""
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if parsed.IsAbs() {
		return parsed.String()
	}

	baseURL, err := url.Parse(base)
	if err != nil {
		return raw
	}

	return baseURL.ResolveReference(parsed).String()
}
