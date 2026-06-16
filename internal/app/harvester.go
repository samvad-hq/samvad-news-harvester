package app

import (
	"context"
	"fmt"
	"time"

	"github.com/samvad-hq/samvad-news-harvester/internal/config"
	"github.com/samvad-hq/samvad-news-harvester/internal/crawler"
	"github.com/samvad-hq/samvad-news-harvester/internal/logger"
	"github.com/samvad-hq/samvad-news-harvester/internal/storage"
	"github.com/samvad-hq/samvad-news-harvester/pkg/providers"
	"github.com/samvad-hq/samvad-news-harvester/pkg/publishers"
)

// Harvester represents the news harvester runtime. It manages the crawl loop,
// coordinating between providers, the crawler service, and publishers. It also
// handles storage initialization and cleanup.
type Harvester struct {
	cfg           *config.Config
	providerReg   *providers.Registry
	fanout        *publishers.Fanout
	crawlService  *crawler.Service
	crawlInterval time.Duration
	log           logger.Logger
	store         storage.Store
}

// NewHarvester builds a harvester runtime from config files.
func NewHarvester(ctx context.Context, cfg *config.Config, log logger.Logger) (*Harvester, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config must not be nil")
	}
	if log == nil {
		log = &logger.NopLogger{}
	}
	if ctx == nil {
		ctx = context.Background()
	}

	providerReg, err := providers.LoadRegistry(cfg.ProvidersFile)
	if err != nil {
		return nil, fmt.Errorf("load providers registry: %w", err)
	}
	providerList := providerReg.All()
	providerIDs := make([]string, 0, len(providerList))
	for _, p := range providerList {
		providerIDs = append(providerIDs, p.ID)
	}
	log.InfoObj("providers registry loaded", "providers_meta", map[string]any{
		"count": len(providerIDs),
		"ids":   providerIDs,
	})

	publisherReg, err := publishers.LoadRegistry(cfg.PublishersFile)
	if err != nil {
		return nil, fmt.Errorf("load publishers registry: %w", err)
	}
	providerRegistry := providers.DefaultFetcherRegistry(nil)

	enabledPublishers := publisherReg.Enabled()
	if len(enabledPublishers) == 0 {
		return nil, fmt.Errorf("no publishers configured")
	}

	pubRegistry := publishers.DefaultRegistry()
	pubClients, err := publishers.BuildAll(ctx, pubRegistry, enabledPublishers, log)
	if err != nil {
		return nil, fmt.Errorf("build publishers: %w", err)
	}
	fanout := publishers.NewFanout(pubClients)
	publisherSummaries := make([]map[string]string, 0, len(enabledPublishers))
	for _, pubCfg := range enabledPublishers {
		publisherSummaries = append(publisherSummaries, map[string]string{
			"id":   pubCfg.ID,
			"type": pubCfg.Type,
		})
	}
	log.InfoObj("publishers registry loaded", "publishers_meta", map[string]any{
		"count":      len(publisherSummaries),
		"publishers": publisherSummaries,
	})

	storeOpts := storage.Options{
		ArticleTTL:      cfg.StorageTTL,
		CleanupInterval: cfg.StorageCleanupInterval,
	}
	store, err := storage.NewStore(cfg.StorageType, cfg.BBoltPath, storeOpts)
	if err != nil {
		return nil, fmt.Errorf("init storage: %w", err)
	}
	log.InfoObj("storage initialized", "storage_config", map[string]any{
		"type":                     cfg.StorageType,
		"path":                     cfg.BBoltPath,
		"article_ttl_seconds":      int(cfg.StorageTTL.Seconds()),
		"cleanup_interval_seconds": int(cfg.StorageCleanupInterval.Seconds()),
	})

	crawlService := crawler.NewService(providerRegistry, fanout, log, store)

	return &Harvester{
		cfg:           cfg,
		providerReg:   providerReg,
		fanout:        fanout,
		crawlService:  crawlService,
		crawlInterval: cfg.CrawlInterval,
		log:           log,
		store:         store,
	}, nil
}

// Run starts the crawl loop until the context is cancelled.
func (h *Harvester) Run(ctx context.Context) error {
	if h == nil || h.crawlService == nil {
		return fmt.Errorf("harvester is not initialized")
	}
	defer h.closeStore()
	providers := h.providerReg.All()
	if len(providers) == 0 {
		h.log.WarnObj("no providers configured; harvester idle", "providers_file", h.cfg.ProvidersFile)
		<-ctx.Done()
		return ctx.Err()
	}

	h.log.InfoObj("harvester loop starting", "harvester_state", map[string]any{
		"providers_count":  len(providers),
		"publishers_count": h.fanout.Size(),
		"crawl_interval":   h.crawlInterval.String(),
	})

	if err := h.runOnce(ctx, providers); err != nil {
		h.log.ErrorObj("initial crawl failed", "error", err)
	}

	ticker := time.NewTicker(h.crawlInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			h.log.InfoObj("harvester loop exiting", "reason", ctx.Err())
			return nil
		case <-ticker.C:
			if err := h.runOnce(ctx, providers); err != nil {
				h.log.ErrorObj("scheduled crawl failed", "error", err)
			}
		}
	}
}

// runOnce performs a single crawl operation across all providers.
func (h *Harvester) runOnce(ctx context.Context, providers []providers.Provider) error {
	start := time.Now()
	h.log.InfoObj("crawl started", "crawl_meta", map[string]any{
		"providers_count": len(providers),
		"started_at":      start.UTC(),
	})
	if err := h.crawlService.Run(ctx, providers); err != nil {
		return err
	}
	h.log.InfoObj("crawl completed", "crawl_meta", map[string]any{
		"providers_count": len(providers),
		"elapsed_ms":      time.Since(start).Milliseconds(),
	})
	return nil
}

// closeStore safely closes the storage backend, logging any errors encountered.
func (h *Harvester) closeStore() {
	if h == nil || h.store == nil {
		return
	}
	if err := h.store.Close(); err != nil {
		h.log.ErrorObj("storage close failed", "error", err)
	}
}
