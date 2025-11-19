package providers

import (
	"context"
	"fmt"
	"strings"

	"github.com/Adda-Baaj/taja-khobor/internal/domain"
)

const anandabazarPatrikaProviderID = "anandabazarpatrika"

// anandabazarPatrikaFetcher fetches Google News sitemap entries for Anandabazar Patrika.
type anandabazarPatrikaFetcher struct {
	client HTTPClient
}

// NewAnandabazarPatrikaFetcher builds a fetcher for Anandabazar Patrika sitemap entries.
func NewAnandabazarPatrikaFetcher(client HTTPClient) Fetcher {
	if client == nil {
		client = DefaultHTTPClient()
	}
	return &anandabazarPatrikaFetcher{client: client}
}

func (f *anandabazarPatrikaFetcher) ID() string {
	return anandabazarPatrikaProviderID
}

func (f *anandabazarPatrikaFetcher) Fetch(ctx context.Context, cfg Provider) ([]domain.Article, error) {
	if !strings.EqualFold(cfg.ID, anandabazarPatrikaProviderID) {
		return nil, fmt.Errorf("anandabazarpatrika fetcher received incompatible provider %q", cfg.ID)
	}
	if strings.TrimSpace(cfg.SourceURL) == "" {
		return nil, fmt.Errorf("anandabazarpatrika provider source_url is empty")
	}

	headers := Headers(cfg)

	raw, err := fetchSitemap(ctx, f.client, cfg.SourceURL, anandabazarPatrikaProviderID, headers)
	if err != nil {
		return nil, err
	}

	urls, err := parseGoogleNewsSitemap(raw)
	if err != nil {
		return nil, fmt.Errorf("decode google news sitemap: %w", err)
	}

	articles := buildArticlesFromSitemap(urls)
	if len(articles) == 0 {
		return nil, fmt.Errorf("anandabazarpatrika sitemap returned no records")
	}

	return articles, nil
}
