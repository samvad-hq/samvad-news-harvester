package providers

import (
	"context"
	"fmt"
	"strings"

	"github.com/Adda-Baaj/taja-khobor/internal/domain"
)

const aajtakProviderID = "aajtak"

// aajtakFetcher fetches Google News sitemap entries for Aaj Tak.
type aajtakFetcher struct {
	client HTTPClient
}

// NewAajtakFetcher builds a fetcher for Aaj Tak sitemap entries.
func NewAajtakFetcher(client HTTPClient) Fetcher {
	if client == nil {
		client = DefaultHTTPClient()
	}
	return &aajtakFetcher{client: client}
}

func (f *aajtakFetcher) ID() string {
	return aajtakProviderID
}

func (f *aajtakFetcher) Fetch(ctx context.Context, cfg Provider) ([]domain.Article, error) {
	if !strings.EqualFold(cfg.ID, aajtakProviderID) {
		return nil, fmt.Errorf("aajtak fetcher received incompatible provider %q", cfg.ID)
	}
	if strings.TrimSpace(cfg.SourceURL) == "" {
		return nil, fmt.Errorf("aajtak provider source_url is empty")
	}

	headers := Headers(cfg)

	raw, err := fetchSitemap(ctx, f.client, cfg.SourceURL, aajtakProviderID, headers)
	if err != nil {
		return nil, err
	}

	urls, err := parseGoogleNewsSitemap(raw)
	if err != nil {
		return nil, fmt.Errorf("decode google news sitemap: %w", err)
	}

	articles := buildArticlesFromSitemap(urls)
	if len(articles) == 0 {
		return nil, fmt.Errorf("aajtak sitemap returned no records")
	}

	return articles, nil
}
