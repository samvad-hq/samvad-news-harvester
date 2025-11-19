package providers

import (
	"context"
	"fmt"
	"strings"

	"github.com/Adda-Baaj/taja-khobor/internal/domain"
)

const dinamalarProviderID = "dinamalar"

// dinamalarFetcher retrieves Google News sitemap entries for Dinamalar.
type dinamalarFetcher struct {
	client HTTPClient
}

// NewDinamalarFetcher builds a fetcher for Dinamalar sitemap entries.
func NewDinamalarFetcher(client HTTPClient) Fetcher {
	if client == nil {
		client = DefaultHTTPClient()
	}
	return &dinamalarFetcher{client: client}
}

func (f *dinamalarFetcher) ID() string {
	return dinamalarProviderID
}

func (f *dinamalarFetcher) Fetch(ctx context.Context, cfg Provider) ([]domain.Article, error) {
	if !strings.EqualFold(cfg.ID, dinamalarProviderID) {
		return nil, fmt.Errorf("dinamalar fetcher received incompatible provider %q", cfg.ID)
	}
	if strings.TrimSpace(cfg.SourceURL) == "" {
		return nil, fmt.Errorf("dinamalar provider source_url is empty")
	}

	headers := Headers(cfg)

	raw, err := fetchSitemap(ctx, f.client, cfg.SourceURL, dinamalarProviderID, headers)
	if err != nil {
		return nil, err
	}

	urls, err := parseGoogleNewsSitemap(raw)
	if err != nil {
		return nil, fmt.Errorf("decode google news sitemap: %w", err)
	}

	articles := buildArticlesFromSitemap(urls)
	if len(articles) == 0 {
		return nil, fmt.Errorf("dinamalar sitemap returned no records")
	}

	return articles, nil
}
