package providers

import (
	"context"
	"fmt"
	"strings"

	"github.com/Adda-Baaj/taja-khobor/internal/domain"
)

const theHinduProviderID = "thehindu"

// theHinduFetcher fetches Google News sitemap entries for The Hindu.
type theHinduFetcher struct {
	client HTTPClient
}

// NewTheHinduFetcher builds a fetcher for The Hindu sitemap entries.
func NewTheHinduFetcher(client HTTPClient) Fetcher {
	if client == nil {
		client = DefaultHTTPClient()
	}
	return &theHinduFetcher{client: client}
}

func (f *theHinduFetcher) ID() string {
	return theHinduProviderID
}

func (f *theHinduFetcher) Fetch(ctx context.Context, cfg Provider) ([]domain.Article, error) {
	if !strings.EqualFold(cfg.ID, theHinduProviderID) {
		return nil, fmt.Errorf("thehindu fetcher received incompatible provider %q", cfg.ID)
	}
	if strings.TrimSpace(cfg.SourceURL) == "" {
		return nil, fmt.Errorf("thehindu provider source_url is empty")
	}

	headers := Headers(cfg)

	raw, err := fetchSitemap(ctx, f.client, cfg.SourceURL, theHinduProviderID, headers)
	if err != nil {
		return nil, err
	}

	urls, err := parseGoogleNewsSitemap(raw)
	if err != nil {
		return nil, fmt.Errorf("decode google news sitemap: %w", err)
	}
	articles := buildArticlesFromSitemap(urls)
	if len(articles) == 0 {
		return nil, fmt.Errorf("thehindu sitemap returned no records")
	}
	return articles, nil
}
