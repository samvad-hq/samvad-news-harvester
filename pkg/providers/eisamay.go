package providers

import (
	"context"
	"fmt"
	"strings"

	"github.com/Adda-Baaj/taja-khobor/internal/domain"
)

const eisamayProviderID = "eisamay"

// eisamayFetcher fetches Google News sitemap entries for Ei Samay.
type eisamayFetcher struct {
	client HTTPClient
}

// NewEisamayFetcher builds a fetcher for Ei Samay sitemap entries.
func NewEisamayFetcher(client HTTPClient) Fetcher {
	if client == nil {
		client = DefaultHTTPClient()
	}
	return &eisamayFetcher{client: client}
}

func (f *eisamayFetcher) ID() string {
	return eisamayProviderID
}

func (f *eisamayFetcher) Fetch(ctx context.Context, cfg Provider) ([]domain.Article, error) {
	if !strings.EqualFold(cfg.ID, eisamayProviderID) {
		return nil, fmt.Errorf("eisamay fetcher received incompatible provider %q", cfg.ID)
	}
	if strings.TrimSpace(cfg.SourceURL) == "" {
		return nil, fmt.Errorf("eisamay provider source_url is empty")
	}

	headers := Headers(cfg)

	raw, err := fetchSitemap(ctx, f.client, cfg.SourceURL, eisamayProviderID, headers)
	if err != nil {
		return nil, err
	}

	urls, err := parseGoogleNewsSitemap(raw)
	if err != nil {
		return nil, fmt.Errorf("decode google news sitemap: %w", err)
	}

	articles := buildArticlesFromSitemap(urls)
	if len(articles) == 0 {
		return nil, fmt.Errorf("eisamay sitemap returned no records")
	}

	return articles, nil
}
