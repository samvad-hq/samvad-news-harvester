package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/samvad-hq/samvad-news-harvester/internal/app"
	"github.com/samvad-hq/samvad-news-harvester/internal/config"
	"github.com/samvad-hq/samvad-news-harvester/internal/logger"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "harvester start failed: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	log, err := logger.Init(cfg)
	if err != nil {
		return fmt.Errorf("init logger: %w", err)
	}
	defer logger.Close()

	logger.InfoObj("harvester starting", "config", cfg)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	harvester, err := app.NewHarvester(ctx, cfg, log)
	if err != nil {
		logger.ErrorObj("failed to initialize harvester", "error", err)
		return err
	}

	if err := harvester.Run(ctx); err != nil {
		return fmt.Errorf("harvester run: %w", err)
	}

	return nil
}
