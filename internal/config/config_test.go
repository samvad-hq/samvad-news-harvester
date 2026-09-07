package config_test

import (
	"log/slog"
	"testing"
	"time"

	"github.com/samvad-hq/samvad-news-harvester/internal/config"
	"github.com/stretchr/testify/require"
)

func TestLoadUsesDefaults(t *testing.T) {
	cfg, err := config.Load()
	require.NoError(t, err)

	require.Equal(t, "samvad-news-harvester", cfg.AppName)
	require.Equal(t, slog.LevelInfo, cfg.LogLevel)
	require.Equal(t, "./configs/sources.yaml", cfg.SourcesFile)
	require.Equal(t, "./configs/sinks.yaml", cfg.SinksFile)
	require.Equal(t, 15*time.Minute, cfg.CrawlInterval)
	require.Equal(t, 8, cfg.SourceConcurrency)
	require.Equal(t, 10, cfg.ArticleConcurrency)
	require.Equal(t, 8, cfg.DeliveryConcurrency)
	require.InDelta(t, 2.0, cfg.PerHostRPS, 0.001)
	require.Equal(t, "bolt", cfg.DedupeBackend)
	require.Equal(t, 120*time.Hour, cfg.DedupeTTL)
	require.Equal(t, 30*time.Second, cfg.FetchTimeout)
}

func TestLoadReadsEnvironment(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("CRAWL_INTERVAL", "90s")
	t.Setenv("SOURCE_CONCURRENCY", "4")
	t.Setenv("DELIVERY_CONCURRENCY", "3")
	t.Setenv("DEDUPE_BACKEND", "none")
	t.Setenv("PER_HOST_RPS", "0.5")

	cfg, err := config.Load()
	require.NoError(t, err)

	require.Equal(t, "production", cfg.Env)
	require.Equal(t, slog.LevelDebug, cfg.LogLevel)
	require.Equal(t, 90*time.Second, cfg.CrawlInterval)
	require.Equal(t, 4, cfg.SourceConcurrency)
	require.Equal(t, 3, cfg.DeliveryConcurrency)
	require.Equal(t, "none", cfg.DedupeBackend)
	require.InDelta(t, 0.5, cfg.PerHostRPS, 0.001)
}

func TestLogLevelParsing(t *testing.T) {
	tests := map[string]slog.Level{
		"debug":   slog.LevelDebug,
		"DEBUG":   slog.LevelDebug,
		"info":    slog.LevelInfo,
		"warn":    slog.LevelWarn,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
	}
	for in, expected := range tests {
		t.Run(in, func(t *testing.T) {
			t.Setenv("LOG_LEVEL", in)
			cfg, err := config.Load()
			require.NoError(t, err)
			require.Equal(t, expected, cfg.LogLevel)
		})
	}
}

func TestLoadRejectsBadValues(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "unparseable duration", key: "CRAWL_INTERVAL", value: "fifteen minutes"},
		{name: "zero interval", key: "CRAWL_INTERVAL", value: "0s"},
		{name: "negative interval", key: "CRAWL_INTERVAL", value: "-5m"},
		{name: "unparseable int", key: "SOURCE_CONCURRENCY", value: "lots"},
		{name: "zero concurrency", key: "SOURCE_CONCURRENCY", value: "0"},
		{name: "unknown log level", key: "LOG_LEVEL", value: "verbose"},
		{name: "unknown dedupe backend", key: "DEDUPE_BACKEND", value: "redis"},
		{name: "zero per-host rate", key: "PER_HOST_RPS", value: "0"},
		{name: "unparseable delivery concurrency", key: "DELIVERY_CONCURRENCY", value: "some"},
		{name: "zero delivery concurrency", key: "DELIVERY_CONCURRENCY", value: "0"},
		{name: "unknown dedupe backend names the valid ones", key: "DEDUPE_BACKEND", value: "memcached"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.key, tt.value)
			_, err := config.Load()
			require.Error(t, err)
		})
	}
}

func TestValidateRejectsBoltWithoutPath(t *testing.T) {
	t.Setenv("DEDUPE_BACKEND", "bolt")
	t.Setenv("DEDUPE_PATH", "")

	_, err := config.Load()
	require.ErrorContains(t, err, "DEDUPE_PATH")
}

func TestNoneBackendDoesNotNeedAPath(t *testing.T) {
	t.Setenv("DEDUPE_BACKEND", "none")
	t.Setenv("DEDUPE_PATH", "")

	cfg, err := config.Load()
	require.NoError(t, err)
	require.Equal(t, "none", cfg.DedupeBackend)
}

func TestLoadReportsBothParseAndValidateErrors(t *testing.T) {
	t.Setenv("SOURCE_CONCURRENCY", "lots")
	t.Setenv("DEDUPE_BACKEND", "redis")

	_, err := config.Load()
	require.Error(t, err)
	require.ErrorContains(t, err, "SOURCE_CONCURRENCY")
	require.ErrorContains(t, err, "DEDUPE_BACKEND")
}

func TestParseErrorDoesNotProduceSpuriousValidateError(t *testing.T) {
	t.Setenv("CRAWL_INTERVAL", "invalid")

	_, err := config.Load()
	require.Error(t, err)
	require.ErrorContains(t, err, "CRAWL_INTERVAL")
	require.ErrorContains(t, err, "not a duration")
	require.NotContains(t, err.Error(), "must be positive")
}

func TestExplicitlyEmptySourcesFileIsError(t *testing.T) {
	t.Setenv("SOURCES_FILE", "")

	_, err := config.Load()
	require.Error(t, err)
	require.ErrorContains(t, err, "SOURCES_FILE")
}

// The redis backend needs a URL and a TTL, and deliberately does not need
// a cleanup interval: Redis expires keys itself, so there is no sweeper to
// schedule. Bolt still requires all three.
func TestRedisBackendValidation(t *testing.T) {
	t.Run("rejects a missing url", func(t *testing.T) {
		t.Setenv("DEDUPE_BACKEND", "redis")
		_, err := config.Load()
		require.ErrorContains(t, err, "DEDUPE_REDIS_URL")
	})

	t.Run("accepts a url", func(t *testing.T) {
		t.Setenv("DEDUPE_BACKEND", "redis")
		t.Setenv("DEDUPE_REDIS_URL", "redis://localhost:6379/0")

		cfg, err := config.Load()
		require.NoError(t, err)
		require.Equal(t, config.DedupeRedis, cfg.DedupeBackend)
		require.Equal(t, "redis://localhost:6379/0", cfg.DedupeRedisURL)
	})

	t.Run("does not require a cleanup interval", func(t *testing.T) {
		t.Setenv("DEDUPE_BACKEND", "redis")
		t.Setenv("DEDUPE_REDIS_URL", "redis://localhost:6379/0")
		t.Setenv("DEDUPE_CLEANUP_INTERVAL", "0s")

		_, err := config.Load()
		require.NoError(t, err, "redis has no sweeper, so the interval is irrelevant to it")
	})

	t.Run("bolt still requires a cleanup interval", func(t *testing.T) {
		t.Setenv("DEDUPE_BACKEND", "bolt")
		t.Setenv("DEDUPE_CLEANUP_INTERVAL", "0s")

		_, err := config.Load()
		require.ErrorContains(t, err, "DEDUPE_CLEANUP_INTERVAL")
	})

	t.Run("an unknown backend names every valid one", func(t *testing.T) {
		t.Setenv("DEDUPE_BACKEND", "memcached")
		_, err := config.Load()
		require.ErrorContains(t, err, "bolt")
		require.ErrorContains(t, err, "redis")
		require.ErrorContains(t, err, "none")
	})
}
