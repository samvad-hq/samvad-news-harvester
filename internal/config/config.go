// Package config loads the harvester's runtime settings from the
// environment, and provides the shared YAML/JSON decoding used by the
// source and sink configuration files.
//
// Settings come from the environment only. An optional configs/.env file
// is read first for local development; real environment variables always
// win over it.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Dedupe backend names accepted by DEDUPE_BACKEND.
const (
	DedupeBolt = "bolt"
	DedupeNone = "none"
)

// Config is the harvester's runtime configuration.
type Config struct {
	// AppName and Env label the process in logs.
	AppName string
	Env     string
	// LogLevel is the minimum level written to stdout.
	LogLevel slog.Level

	// SourcesFile and SinksFile are paths to the YAML or JSON files
	// describing what to crawl and where to deliver.
	SourcesFile string
	SinksFile   string

	// CrawlInterval is how often the full source list is crawled.
	CrawlInterval time.Duration
	// SourceConcurrency bounds how many sources are crawled at once.
	SourceConcurrency int
	// ArticleConcurrency bounds in-flight metadata scrapes per source.
	ArticleConcurrency int
	// DeliveryConcurrency bounds in-flight sink deliveries per source.
	DeliveryConcurrency int
	// PerHostRPS caps requests per second to any single host.
	PerHostRPS float64

	// DedupeBackend is DedupeBolt or DedupeNone.
	DedupeBackend string
	// DedupePath is the bbolt file, required when the backend is bolt.
	DedupePath string
	// DedupeTTL is how long an article ID is remembered.
	DedupeTTL time.Duration
	// DedupeCleanupInterval is how often expired IDs are swept.
	DedupeCleanupInterval time.Duration

	// FetchTimeout bounds one sitemap request.
	FetchTimeout time.Duration
	// ScrapeTimeout bounds one article metadata request.
	ScrapeTimeout time.Duration
}

// Load reads configuration from the environment and validates it.
func Load() (*Config, error) {
	// A missing .env is normal in production, so the error is ignored.
	_ = godotenv.Load("configs/.env")

	var errs []error
	get := func(fn func() error) {
		if err := fn(); err != nil {
			errs = append(errs, err)
		}
	}

	cfg := &Config{
		AppName:       env("APP_NAME", "samvad-news-harvester"),
		Env:           env("APP_ENV", "development"),
		SourcesFile:   envIfSetElseDefault("SOURCES_FILE", "./configs/sources.yaml"),
		SinksFile:     envIfSetElseDefault("SINKS_FILE", "./configs/sinks.yaml"),
		DedupeBackend: strings.ToLower(env("DEDUPE_BACKEND", DedupeBolt)),
		DedupePath:    envIfSetElseDefault("DEDUPE_PATH", "./data/dedupe.db"),
	}

	get(func() (err error) { cfg.LogLevel, err = envLevel("LOG_LEVEL", slog.LevelInfo); return })
	get(func() (err error) { cfg.CrawlInterval, err = envDuration("CRAWL_INTERVAL", 15*time.Minute); return })
	get(func() (err error) { cfg.SourceConcurrency, err = envInt("SOURCE_CONCURRENCY", 8); return })
	get(func() (err error) { cfg.ArticleConcurrency, err = envInt("ARTICLE_CONCURRENCY", 10); return })
	get(func() (err error) { cfg.DeliveryConcurrency, err = envInt("DELIVERY_CONCURRENCY", 8); return })
	get(func() (err error) { cfg.PerHostRPS, err = envFloat("PER_HOST_RPS", 2); return })
	get(func() (err error) { cfg.DedupeTTL, err = envDuration("DEDUPE_TTL", 120*time.Hour); return })
	get(func() (err error) {
		cfg.DedupeCleanupInterval, err = envDuration("DEDUPE_CLEANUP_INTERVAL", 12*time.Hour)
		return
	})
	get(func() (err error) { cfg.FetchTimeout, err = envDuration("FETCH_TIMEOUT", 30*time.Second); return })
	get(func() (err error) { cfg.ScrapeTimeout, err = envDuration("SCRAPE_TIMEOUT", 15*time.Second); return })

	parseErr := errors.Join(errs...)
	// Validate runs even when parsing failed, so an operator sees every
	// problem in one pass. Failed parses leave their field at the default,
	// which is valid, so Validate does not pile a spurious complaint on top
	// of the parse error for the same variable.
	if err := errors.Join(parseErr, cfg.Validate()); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate reports every problem with the configuration at once, so a
// misconfigured deployment is fixed in one pass rather than one restart
// per mistake. It is also what the validate subcommand calls.
func (c *Config) Validate() error {
	var errs []error

	if c.SourcesFile == "" {
		errs = append(errs, errors.New("SOURCES_FILE must not be empty"))
	}
	if c.SinksFile == "" {
		errs = append(errs, errors.New("SINKS_FILE must not be empty"))
	}
	if c.CrawlInterval <= 0 {
		errs = append(errs, errors.New("CRAWL_INTERVAL must be positive"))
	}
	if c.SourceConcurrency < 1 {
		errs = append(errs, errors.New("SOURCE_CONCURRENCY must be at least 1"))
	}
	if c.ArticleConcurrency < 1 {
		errs = append(errs, errors.New("ARTICLE_CONCURRENCY must be at least 1"))
	}
	if c.DeliveryConcurrency < 1 {
		errs = append(errs, errors.New("DELIVERY_CONCURRENCY must be at least 1"))
	}
	if c.PerHostRPS <= 0 {
		errs = append(errs, errors.New("PER_HOST_RPS must be positive"))
	}
	if c.FetchTimeout <= 0 {
		errs = append(errs, errors.New("FETCH_TIMEOUT must be positive"))
	}
	if c.ScrapeTimeout <= 0 {
		errs = append(errs, errors.New("SCRAPE_TIMEOUT must be positive"))
	}

	switch c.DedupeBackend {
	case DedupeNone:
	case DedupeBolt:
		if strings.TrimSpace(c.DedupePath) == "" {
			errs = append(errs, errors.New("DEDUPE_PATH is required when DEDUPE_BACKEND is bolt"))
		}
		if c.DedupeTTL <= 0 {
			errs = append(errs, errors.New("DEDUPE_TTL must be positive"))
		}
		if c.DedupeCleanupInterval <= 0 {
			errs = append(errs, errors.New("DEDUPE_CLEANUP_INTERVAL must be positive"))
		}
	default:
		errs = append(errs, fmt.Errorf("DEDUPE_BACKEND %q is not supported (use %q or %q)",
			c.DedupeBackend, DedupeBolt, DedupeNone))
	}

	return errors.Join(errs...)
}

// env returns the trimmed value of key, or fallback when it is unset or blank.
func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// envIfSetElseDefault returns the trimmed value of key if set, or the default
// if the env var is not set. This lets us distinguish between unset (use default)
// and explicitly empty (validation will catch it).
func envIfSetElseDefault(key, defaultVal string) string {
	v, ok := os.LookupEnv(key)
	if !ok {
		// env var not set, use default
		return defaultVal
	}
	// env var is set (even if empty), trim and return
	return strings.TrimSpace(v)
}

// envDuration parses key as a Go duration string such as "15m" or "90s".
func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return fallback, fmt.Errorf("%s: %q is not a duration such as 15m or 90s", key, raw)
	}
	return d, nil
}

// envInt parses key as a base-10 integer.
func envInt(key string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback, fmt.Errorf("%s: %q is not an integer", key, raw)
	}
	return n, nil
}

// envFloat parses key as a floating-point number.
func envFloat(key string, fallback float64) (float64, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fallback, fmt.Errorf("%s: %q is not a number", key, raw)
	}
	return f, nil
}

// envLevel parses key as a slog level name.
func envLevel(key string, fallback slog.Level) (slog.Level, error) {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch raw {
	case "":
		return fallback, nil
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return fallback, fmt.Errorf("%s: %q is not a level (use debug, info, warn or error)", key, raw)
	}
}
