package providers

import (
	"context"
	"fmt"
	"strings"

	"github.com/Adda-Baaj/taja-khobor/internal/domain"
)

const financialExpressProviderID = "financialexpress"

// financialExpressFetcher fetches Google News sitemap entries for Financial Express.
type financialExpressFetcher struct {
	client HTTPClient
}

// NewFinancialExpressFetcher builds a fetcher for Financial Express sitemap entries.
func NewFinancialExpressFetcher(client HTTPClient) Fetcher {
	if client == nil {
		client = DefaultHTTPClient()
	}
	return &financialExpressFetcher{client: client}
}

func (f *financialExpressFetcher) ID() string {
	return financialExpressProviderID
}

func (f *financialExpressFetcher) Fetch(ctx context.Context, cfg Provider) ([]domain.Article, error) {
	if !strings.EqualFold(cfg.ID, financialExpressProviderID) {
		return nil, fmt.Errorf("financialexpress fetcher received incompatible provider %q", cfg.ID)
	}
	if strings.TrimSpace(cfg.SourceURL) == "" {
		return nil, fmt.Errorf("financialexpress provider source_url is empty")
	}

	headers := Headers(cfg)

	raw, err := fetchSitemap(ctx, f.client, cfg.SourceURL, financialExpressProviderID, headers)
	if err != nil {
		return nil, err
	}

	urls, err := parseGoogleNewsSitemap(raw)
	if err != nil {
		return nil, fmt.Errorf("decode google news sitemap: %w", err)
	}
	articles := buildArticlesFromSitemap(urls)
	if len(articles) == 0 {
		return nil, fmt.Errorf("financialexpress sitemap returned no records")
	}
	return articles, nil
}
