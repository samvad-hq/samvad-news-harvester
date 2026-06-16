package providers

import (
	"context"

	"github.com/samvad-hq/samvad-news-harvester/internal/domain"
	"github.com/samvad-hq/samvad-news-harvester/pkg/httpclient"
)

// Fetcher defines the interface for news provider fetchers.
// Implementations should handle fetching articles from specific provider types.
type Fetcher interface {
	ID() string
	Fetch(ctx context.Context, cfg Provider) ([]domain.Article, error)
}

// FetcherRegistry resolves the fetcher implementation for a given provider config.
type FetcherRegistry interface {
	FetcherFor(cfg Provider) (Fetcher, error)
}

// HTTPClient aliases the shared httpclient.Client interface for clarity within providers.
type HTTPClient = httpclient.Client
