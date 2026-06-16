package config

import (
	"fmt"
	"time"

	"github.com/joho/godotenv"
	"github.com/spf13/viper"
)

// Config holds the application configuration loaded from files and environment variables.
type Config struct {
	AppName              string        `mapstructure:"app_name"`
	Env                  string        `mapstructure:"app_env"`
	LogLevel             string        `mapstructure:"log_level"`
	ProvidersFile        string        `mapstructure:"providers_file"`
	PublishersFile       string        `mapstructure:"publishers_file"`
	CrawlIntervalSeconds int64         `mapstructure:"crawl_interval"`
	CrawlInterval        time.Duration `mapstructure:"-"`

	StorageType            string        `mapstructure:"storage_type"`
	BBoltPath              string        `mapstructure:"bbolt_path"`
	StorageTTLSeconds      int64         `mapstructure:"storage_ttl_seconds"`
	StorageCleanupSeconds  int64         `mapstructure:"storage_cleanup_interval_seconds"`
	StorageTTL             time.Duration `mapstructure:"-"`
	StorageCleanupInterval time.Duration `mapstructure:"-"`
}

// Load reads configuration from environment variables and config files.
func Load() (*Config, error) {
	_ = godotenv.Load("configs/.env")

	v := viper.New()

	v.SetDefault("app_name", "samvad-news-harvester")
	v.SetDefault("app_env", "development")
	v.SetDefault("log_level", "info")
	v.SetDefault("providers_file", "./configs/providers.yaml")
	v.SetDefault("publishers_file", "./configs/publishers.yaml")
	v.SetDefault("crawl_interval", 900) // seconds
	v.SetDefault("storage_type", "bbolt")
	v.SetDefault("bbolt_path", "./data/cache.db")
	v.SetDefault("storage_ttl_seconds", int64((5*24*time.Hour)/time.Second))
	v.SetDefault("storage_cleanup_interval_seconds", int64((12*time.Hour)/time.Second))

	v.AutomaticEnv()

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	if cfg.CrawlIntervalSeconds <= 0 {
		return nil, fmt.Errorf("invalid crawl_interval (must be positive seconds)")
	}
	cfg.CrawlInterval = time.Duration(cfg.CrawlIntervalSeconds) * time.Second

	if cfg.StorageTTLSeconds <= 0 {
		return nil, fmt.Errorf("invalid storage_ttl_seconds (must be positive seconds)")
	}
	if cfg.StorageCleanupSeconds <= 0 {
		return nil, fmt.Errorf("invalid storage_cleanup_interval_seconds (must be positive seconds)")
	}
	cfg.StorageTTL = time.Duration(cfg.StorageTTLSeconds) * time.Second
	cfg.StorageCleanupInterval = time.Duration(cfg.StorageCleanupSeconds) * time.Second

	return &cfg, nil
}
