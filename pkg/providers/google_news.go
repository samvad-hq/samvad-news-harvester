package providers

import (
	"context"
	"fmt"
	"strings"

	"github.com/samvad-hq/samvad-news-harvester/internal/domain"
	"github.com/samvad-hq/samvad-news-harvester/pkg/httpclient"
)

// googleNewsFetcher implements Fetcher for Google News sitemap providers.
type googleNewsFetcher struct {
	clients *httpclient.ProxyCache
}

// NewGoogleNewsFetcher builds a Fetcher for Google News sitemap providers. A nil
// client defaults to a direct (no-proxy) client; per-provider proxies are
// resolved at fetch time from the provider config.
func NewGoogleNewsFetcher(client HTTPClient) Fetcher {
	return &googleNewsFetcher{clients: httpclient.NewProxyCache(defaultFetchTimeout, client)}
}

// ID returns the provider type for the Google News fetcher.
func (f *googleNewsFetcher) ID() string {
	return ProviderTypeGoogleNews
}

// Fetch retrieves articles from a Google News sitemap provider.
func (f *googleNewsFetcher) Fetch(ctx context.Context, cfg Provider) ([]domain.Article, error) {
	if !strings.EqualFold(cfg.Type, ProviderTypeGoogleNews) {
		return nil, fmt.Errorf("google news fetcher received incompatible provider type %q", cfg.Type)
	}
	if strings.TrimSpace(cfg.SourceURL) == "" {
		return nil, fmt.Errorf("provider %q source_url is empty", cfg.ID)
	}

	headers := Headers(cfg)
	client := f.clients.For(cfg.Proxy)

	urls, err := f.fetchGoogleNewsURLs(ctx, client, cfg, cfg.SourceURL, headers, nil)
	if err != nil {
		return nil, err
	}

	articles := buildArticlesFromSitemap(cfg.ID, urls)
	if len(articles) == 0 {
		return nil, fmt.Errorf("%s sitemap returned no records", cfg.ID)
	}
	return articles, nil
}

// fetchGoogleNewsURLs resolves the given sitemap URL into article entries, following sitemap indexes if necessary.
func (f *googleNewsFetcher) fetchGoogleNewsURLs(ctx context.Context, client HTTPClient, cfg Provider, url string, headers map[string]string, visited map[string]struct{}) ([]googleNewsURL, error) {
	if visited == nil {
		visited = make(map[string]struct{})
	}
	if _, seen := visited[url]; seen {
		return nil, nil
	}
	visited[url] = struct{}{}

	raw, err := fetchSitemap(ctx, client, url, cfg.ID, headers)
	if err != nil {
		return nil, err
	}

	urls, err := parseGoogleNewsSitemap(raw)
	if err != nil {
		return nil, fmt.Errorf("decode google news sitemap: %w", err)
	}
	if len(urls) > 0 {
		return urls, nil
	}

	indexURLs, err := parseSitemapIndex(raw)
	if err != nil {
		return nil, fmt.Errorf("decode sitemap index: %w", err)
	}
	if len(indexURLs) == 0 {
		return nil, nil
	}

	var all []googleNewsURL
	for _, indexURL := range indexURLs {
		indexURL = strings.TrimSpace(indexURL)
		if indexURL == "" {
			continue
		}

		nested, err := f.fetchGoogleNewsURLs(ctx, client, cfg, indexURL, headers, visited)
		if err != nil {
			return nil, err
		}
		all = append(all, nested...)
	}
	return all, nil
}
