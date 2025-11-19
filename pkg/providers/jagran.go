package providers

import (
	"context"
	"fmt"
	"strings"

	"github.com/Adda-Baaj/taja-khobor/internal/domain"
)

const jagranProviderID = "jagran"

// jagranFetcher retrieves Google News sitemap entries for Jagran.
type jagranFetcher struct {
	client HTTPClient
}

// NewJagranFetcher builds a fetcher for Jagran sitemap entries.
func NewJagranFetcher(client HTTPClient) Fetcher {
	if client == nil {
		client = DefaultHTTPClient()
	}
	return &jagranFetcher{client: client}
}

func (f *jagranFetcher) ID() string {
	return jagranProviderID
}

func (f *jagranFetcher) Fetch(ctx context.Context, cfg Provider) ([]domain.Article, error) {
	if !strings.EqualFold(cfg.ID, jagranProviderID) {
		return nil, fmt.Errorf("jagran fetcher received incompatible provider %q", cfg.ID)
	}
	if strings.TrimSpace(cfg.SourceURL) == "" {
		return nil, fmt.Errorf("jagran provider source_url is empty")
	}

	headers := Headers(cfg)

	raw, err := fetchSitemap(ctx, f.client, cfg.SourceURL, jagranProviderID, headers)
	if err != nil {
		return nil, err
	}

	urls, err := parseGoogleNewsSitemap(raw)
	if err != nil {
		return nil, fmt.Errorf("decode google news sitemap: %w", err)
	}

	articles := buildArticlesFromSitemap(urls)
	if len(articles) == 0 {
		return nil, fmt.Errorf("jagran sitemap returned no records")
	}

	return articles, nil
}
