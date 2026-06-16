package storage

import (
	"fmt"
	"strings"
	"time"
)

// Package storage provides local DB/cache abstraction.

// Store tracks published article IDs.
type Store interface {
	Close() error
	SeenArticle(id string) (bool, error)
	MarkArticle(id string) error
}

// Options controls retention characteristics for concrete store implementations.
type Options struct {
	ArticleTTL      time.Duration
	CleanupInterval time.Duration
}

const (
	defaultArticleTTL      = 5 * 24 * time.Hour
	defaultCleanupInterval = 12 * time.Hour
)

// NewStore creates the configured storage backend.
func NewStore(typ, path string, opts Options) (Store, error) {
	typ = strings.TrimSpace(strings.ToLower(typ))
	opts = normalizeOptions(opts)

	switch typ {
	case "", "none", "disabled":
		return noopStore{}, nil
	case "bbolt":
		if strings.TrimSpace(path) == "" {
			return nil, fmt.Errorf("bbolt storage requires a path")
		}
		return openBolt(path, opts)
	default:
		return nil, fmt.Errorf("unsupported storage type %q", typ)
	}
}

func normalizeOptions(opts Options) Options {
	if opts.ArticleTTL <= 0 {
		opts.ArticleTTL = defaultArticleTTL
	}
	if opts.CleanupInterval <= 0 {
		opts.CleanupInterval = defaultCleanupInterval
	}
	return opts
}

type noopStore struct{}

func (noopStore) Close() error                     { return nil }
func (noopStore) SeenArticle(string) (bool, error) { return false, nil }
func (noopStore) MarkArticle(string) error         { return nil }
