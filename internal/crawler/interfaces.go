package crawler

import (
	"context"

	"github.com/samvad-hq/samvad-news-harvester/internal/domain"
	"github.com/samvad-hq/samvad-news-harvester/pkg/providers"
	"github.com/samvad-hq/samvad-news-harvester/pkg/publishers"
)

// ArticleScraper enriches crawled articles with metadata (e.g., OG tags).
type ArticleScraper interface {
	Enrich(ctx context.Context, cfg providers.Provider, articles []domain.Article) []domain.Article
}

// EventPublisher publishes enriched articles downstream.
type EventPublisher interface {
	Publish(ctx context.Context, evt publishers.Event) (int, error)
}

// ArticleDeduper tracks which articles have been published already.
type ArticleDeduper interface {
	SeenArticle(id string) (bool, error)
	MarkArticle(id string) error
}
