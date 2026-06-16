package crawler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/samvad-hq/samvad-news-harvester/internal/domain"
	"github.com/samvad-hq/samvad-news-harvester/internal/logger"
	"github.com/samvad-hq/samvad-news-harvester/pkg/providers"
	"github.com/samvad-hq/samvad-news-harvester/pkg/publishers"
)

const maxProviderWorkers = 10

// Service orchestrates crawling of news providers, article enrichment, and publishing.
type Service struct {
	processor *ProviderProcessor
	log       logger.Logger
}

// NewService builds a crawler service with the given fetcher registry and event publisher.
func NewService(reg providers.FetcherRegistry, pub EventPublisher, log logger.Logger, deduper ArticleDeduper) *Service {
	if log == nil {
		log = logger.NopLogger{}
	}

	scraper := NewScraper(nil, log)

	processor := NewProviderProcessor(reg, scraper, pub, log, deduper)
	return &Service{
		processor: processor,
		log:       log,
	}
}

// Run starts the crawl loop until the context is cancelled.
func (s *Service) Run(ctx context.Context, cfgs []providers.Provider) error {
	if s == nil || s.processor == nil {
		return fmt.Errorf("crawler service is not initialized")
	}

	if len(cfgs) == 0 {
		return fmt.Errorf("no providers configured for crawling")
	}

	errs := s.runAll(ctx, cfgs)
	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	return nil
}

// runAll concurrently processes all providers using a pool of workers.
func (s *Service) runAll(ctx context.Context, cfgs []providers.Provider) []error {
	workerCount := min(len(cfgs), maxProviderWorkers)
	if workerCount == 0 {
		return nil
	}

	cfgCh := make(chan providers.Provider)
	errCh := make(chan error, len(cfgs))

	var wg sync.WaitGroup
	for workerID := range workerCount {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			s.worker(ctx, cfgCh, errCh, id)
		}(workerID)
	}

	for _, cfg := range cfgs {
		if ctx.Err() != nil {
			break
		}
		cfgCh <- cfg
	}
	close(cfgCh)

	wg.Wait()
	close(errCh)

	errs := make([]error, 0, len(cfgs))
	for err := range errCh {
		errs = append(errs, err)
	}

	return errs
}

// worker processes providers from the channel and reports errors.
func (s *Service) worker(ctx context.Context, cfgCh <-chan providers.Provider, errCh chan<- error, workerID int) {
	for cfg := range cfgCh {
		if ctx.Err() != nil {
			return
		}
		if err := s.processor.Process(ctx, cfg, workerID); err != nil {
			errCh <- err
			s.log.ErrorObj("provider crawl failed", "provider_error", map[string]any{
				"worker_id":   workerID,
				"provider_id": cfg.ID,
				"error":       err.Error(),
			})
		}
	}
}

// ProviderProcessor fetches, enriches, and publishes provider articles.
type ProviderProcessor struct {
	registry  providers.FetcherRegistry
	scraper   ArticleScraper
	publisher EventPublisher
	deduper   ArticleDeduper
	log       logger.Logger
}

// NewProviderProcessor builds a provider processor with the given fetcher registry, scraper, event publisher, logger, and article deduper.
func NewProviderProcessor(reg providers.FetcherRegistry, scraper ArticleScraper, pub EventPublisher, log logger.Logger, deduper ArticleDeduper) *ProviderProcessor {
	if log == nil {
		log = logger.NopLogger{}
	}
	return &ProviderProcessor{
		registry:  reg,
		scraper:   scraper,
		publisher: pub,
		deduper:   deduper,
		log:       log,
	}
}

// Process fetches, enriches, and publishes articles for the given provider configuration.
func (p *ProviderProcessor) Process(ctx context.Context, cfg providers.Provider, workerID int) error {
	if p == nil || p.registry == nil {
		return fmt.Errorf("provider processor not initialized")
	}

	start := time.Now()
	fetcher, err := p.registry.FetcherFor(cfg)
	if err != nil {
		return fmt.Errorf("resolve fetcher for provider %s: %w", cfg.ID, err)
	}

	articles, err := fetcher.Fetch(ctx, cfg)
	if err != nil {
		return fmt.Errorf("fetch provider %s: %w", cfg.ID, err)
	}

	fetchedCount := len(articles)
	if p.deduper != nil && fetchedCount > 0 {
		articles = p.filterNewArticles(cfg, articles)
	}

	if p.scraper != nil {
		articles = p.scraper.Enrich(ctx, cfg, articles)
	}

	if len(articles) == 0 {
		p.log.InfoObj("provider crawl completed", "provider_result", map[string]any{
			"worker_id":          workerID,
			"provider_id":        cfg.ID,
			"articles_fetched":   fetchedCount,
			"articles_fresh":     0,
			"articles_published": 0,
			"elapsed_ms":         time.Since(start).Milliseconds(),
		})
		return nil
	}

	published := 0
	if count, err := p.publishArticles(ctx, cfg, articles); err != nil {
		return fmt.Errorf("publish provider %s articles: %w", cfg.ID, err)
	} else {
		published = count
	}

	p.log.InfoObj("provider crawl completed", "provider_result", map[string]any{
		"worker_id":          workerID,
		"provider_id":        cfg.ID,
		"articles_fetched":   fetchedCount,
		"articles_fresh":     len(articles),
		"articles_published": published,
		"elapsed_ms":         time.Since(start).Milliseconds(),
	})
	return nil
}

// publishArticles publishes the given articles for the provider and returns the count of successfully published articles and any errors.
func (p *ProviderProcessor) publishArticles(ctx context.Context, cfg providers.Provider, articles []domain.Article) (int, error) {
	if p.publisher == nil || len(articles) == 0 {
		return 0, nil
	}

	var errs []error
	published := 0
	for _, art := range articles {
		evt := publishers.NewEvent(cfg.ID, cfg.Name, art)
		successful, err := p.publisher.Publish(ctx, evt)
		if err != nil {
			errs = append(errs, fmt.Errorf("article %s: %w", art.ID, err))
			p.log.ErrorObj("failed to publish article", "publisher_error", map[string]any{
				"provider_id": cfg.ID,
				"article_id":  art.ID,
				"error":       err.Error(),
			})
		}
		if successful > 0 {
			published++
			if p.deduper != nil {
				if markErr := p.deduper.MarkArticle(art.ID); markErr != nil {
					p.log.ErrorObj("failed to cache published article", "dedupe_error", map[string]any{
						"provider_id": cfg.ID,
						"article_id":  art.ID,
						"error":       markErr.Error(),
					})
				}
			}
		}
	}

	return published, errors.Join(errs...)
}

// filterNewArticles filters out articles that have already been published according to the deduper.
func (p *ProviderProcessor) filterNewArticles(cfg providers.Provider, articles []domain.Article) []domain.Article {
	if p.deduper == nil || len(articles) == 0 {
		return articles
	}

	fresh := make([]domain.Article, 0, len(articles))
	for _, art := range articles {
		seen, err := p.deduper.SeenArticle(art.ID)
		if err != nil {
			p.log.ErrorObj("dedupe lookup failed", "dedupe_error", map[string]any{
				"provider_id": cfg.ID,
				"article_id":  art.ID,
				"error":       err.Error(),
			})
			fresh = append(fresh, art)
			continue
		}
		if seen {
			p.log.DebugObj("article skipped (already published)", "article_skip", map[string]any{
				"provider_id": cfg.ID,
				"article_id":  art.ID,
			})
			continue
		}
		fresh = append(fresh, art)
	}
	return fresh
}
