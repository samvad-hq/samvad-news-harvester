package providers

import (
	"context"
	"fmt"
	"strings"

	"github.com/Adda-Baaj/taja-khobor/internal/domain"
)

const (
	ndtvProviderID = "ndtv"
)

// ndtvFetcher fetches Google News sitemap entries for NDTV.
type ndtvFetcher struct {
	client HTTPClient
}

// NewNDTVFetcher builds a fetcher for NDTV sitemap entries.
func NewNDTVFetcher(client HTTPClient) Fetcher {
	if client == nil {
		client = DefaultHTTPClient()
	}
	return &ndtvFetcher{
		client: client,
	}
}

func (f *ndtvFetcher) ID() string {
	return ndtvProviderID
}

func (f *ndtvFetcher) Fetch(ctx context.Context, cfg Provider) ([]domain.Article, error) {
	if !strings.EqualFold(cfg.ID, ndtvProviderID) {
		return nil, fmt.Errorf("ndtv fetcher received incompatible provider %q", cfg.ID)
	}
	if strings.TrimSpace(cfg.SourceURL) == "" {
		return nil, fmt.Errorf("ndtv provider source_url is empty")
	}

	headers := Headers(cfg)

	raw, err := fetchSitemap(ctx, f.client, cfg.SourceURL, ndtvProviderID, headers)
	if err != nil {
		return nil, err
	}

	urls, err := parseGoogleNewsSitemap(raw)
	if err != nil {
		return nil, fmt.Errorf("decode google news sitemap: %w", err)
	}
	articles := buildArticlesFromSitemap(urls)
	if len(articles) == 0 {
		return nil, fmt.Errorf("ndtv sitemap returned no records")
	}
	return articles, nil
}
