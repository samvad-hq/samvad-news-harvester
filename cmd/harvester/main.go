// Command harvester crawls news sitemaps and delivers article events to
// the configured sinks.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/samvad-hq/samvad-news-harvester/internal/config"
	"github.com/samvad-hq/samvad-news-harvester/internal/dedupe"
	"github.com/samvad-hq/samvad-news-harvester/internal/enrich"
	"github.com/samvad-hq/samvad-news-harvester/internal/harvest"
	"github.com/samvad-hq/samvad-news-harvester/internal/httpx"
	"github.com/samvad-hq/samvad-news-harvester/internal/sink"
	"github.com/samvad-hq/samvad-news-harvester/internal/source"
	"golang.org/x/time/rate"
)

func main() {
	validateOnly := flag.Bool("validate", false, "check the configuration and exit without crawling")
	once := flag.Bool("once", false, "run a single crawl and exit")
	showVersion := flag.Bool("version", false, "print the build version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(buildVersion())
		return
	}

	if err := run(*validateOnly, *once); err != nil {
		fmt.Fprintf(os.Stderr, "harvester: %v\n", err)
		os.Exit(1)
	}
}

// run wires the service together and starts it.
//
// A cancelled context is a clean shutdown and returns nil, so SIGTERM
// exits 0. The old code returned ctx.Err() from one path, which made a
// normal stop look like a crash to an orchestrator and produced a restart
// loop on a misconfigured deployment.
func run(validateOnly, once bool) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)

	sources, err := source.LoadFile(cfg.SourcesFile)
	if err != nil {
		return err
	}
	sinkCfgs, err := sink.LoadFile(cfg.SinksFile)
	if err != nil {
		return err
	}

	if validateOnly {
		fmt.Printf("configuration is valid: %d sources, %d sinks\n", len(sources), len(sinkCfgs))
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	sinks, closeSinks, err := sink.Build(ctx, sinkCfgs, log)
	if err != nil {
		return err
	}
	defer func() {
		if err := closeSinks(); err != nil {
			log.Error("closing sinks failed", "error", err)
		}
	}()

	deduper, closeDeduper, err := openDeduper(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() {
		if err := closeDeduper(); err != nil {
			log.Error("closing the dedupe store failed", "error", err)
		}
	}()

	fetchClients := httpx.NewProxyCache(cfg.FetchTimeout, httpx.DefaultMaxBodyBytes)
	scrapeClients := httpx.NewProxyCache(cfg.ScrapeTimeout, maxArticleBodyBytes)
	limiter := httpx.NewHostLimiter(rate.Limit(cfg.PerHostRPS), 1)

	harvester, err := harvest.New(harvest.Deps{
		Sources: sources,
		Fetchers: map[string]harvest.Fetcher{
			source.TypeNewsSitemap: source.NewSitemap(fetchClients, log),
		},
		Enricher:            enrich.NewScraper(scrapeClients, limiter, cfg.ArticleConcurrency, log),
		Deduper:             deduper,
		Sinks:               sink.NewFanout(sinks, log),
		Log:                 log,
		Interval:            cfg.CrawlInterval,
		SourceConcurrency:   cfg.SourceConcurrency,
		DeliveryConcurrency: cfg.DeliveryConcurrency,
	})
	if err != nil {
		return err
	}

	if once {
		err := harvester.RunOnce(ctx)
		if err == nil {
			return nil
		}
		log.Error("crawl finished with errors", "error", err)
		// Three of the 26 configured sources answer 403 by design, so
		// exiting non-zero whenever any source fails would give a cron
		// wrapper a permanently red exit code it learns to ignore. Only
		// a total failure — every source, not just some — is worth
		// surfacing as a failed run; see the README's exit-code section.
		if harvest.FailedSources(err) >= len(sources) {
			return fmt.Errorf("every configured source failed")
		}
		return nil
	}

	if err := harvester.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// maxArticleBodyBytes caps an article page. Metadata lives in <head>, so
// 1 MiB is ample and keeps a pathological page from occupying memory.
const maxArticleBodyBytes int64 = 1 << 20

// openDeduper builds the configured dedupe store and its closer.
//
// Config.Validate has already rejected any backend name that is not one of
// these three, and has checked that the fields each one needs are present.
func openDeduper(ctx context.Context, cfg *config.Config) (harvest.Deduper, func() error, error) {
	switch cfg.DedupeBackend {
	case config.DedupeNone:
		var store dedupe.Noop
		return store, store.Close, nil

	case config.DedupeRedis:
		store, err := dedupe.OpenRedis(ctx, cfg.DedupeRedisURL, cfg.DedupeTTL)
		if err != nil {
			return nil, nil, err
		}
		return store, store.Close, nil

	default:
		store, err := dedupe.OpenBolt(cfg.DedupePath, cfg.DedupeTTL, cfg.DedupeCleanupInterval)
		if err != nil {
			return nil, nil, err
		}
		return store, store.Close, nil
	}
}
