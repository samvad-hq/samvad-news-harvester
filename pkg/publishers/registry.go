package publishers

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// Builder creates a Publisher from a config entry.
type Builder func(ctx context.Context, cfg PublisherConfig, log Logger) (Publisher, error)

// Registry maps publisher types to builders.
type Registry interface {
	Register(typ string, builder Builder)
	PublisherFor(ctx context.Context, cfg PublisherConfig, log Logger) (Publisher, error)
}

type registry struct {
	mu       sync.RWMutex
	builders map[string]Builder
}

// NewRegistry returns a registry with optional pre-registered builders.
func NewRegistry(builders map[string]Builder) Registry {
	r := &registry{
		builders: make(map[string]Builder),
	}
	for typ, b := range builders {
		r.Register(typ, b)
	}
	return r
}

// Register associates a builder with a publisher type.
func (r *registry) Register(typ string, builder Builder) {
	if typ = strings.TrimSpace(strings.ToLower(typ)); typ == "" || builder == nil {
		return
	}

	r.mu.Lock()
	r.builders[typ] = builder
	r.mu.Unlock()
}

// PublisherFor returns the publisher built for the provided config.
func (r *registry) PublisherFor(ctx context.Context, cfg PublisherConfig, log Logger) (Publisher, error) {
	if cfg.Type == "" {
		return nil, fmt.Errorf("publisher %q has no type configured", cfg.ID)
	}

	r.mu.RLock()
	builder := r.builders[strings.ToLower(cfg.Type)]
	r.mu.RUnlock()

	if builder == nil {
		return nil, fmt.Errorf("no publisher registered for type %q", cfg.Type)
	}
	return builder(ctx, cfg, log)
}

// DefaultRegistry wires up known publishers.
func DefaultRegistry() Registry {
	builders := map[string]Builder{
		TypeHTTP:  newHTTPPublisher,
		TypeQueue: newQueuePublisher,
	}
	return NewRegistry(builders)
}

// BuildAll instantiates publishers for configs using the registry.
func BuildAll(ctx context.Context, reg Registry, cfgs []PublisherConfig, log Logger) ([]Publisher, error) {
	if reg == nil || len(cfgs) == 0 {
		return nil, nil
	}

	if ctx == nil {
		ctx = context.Background()
	}
	log = ensureLogger(log)

	var pubs []Publisher
	for _, cfg := range cfgs {
		pub, err := reg.PublisherFor(ctx, cfg, log)
		if err != nil {
			return nil, err
		}
		pubs = append(pubs, pub)
	}
	return pubs, nil
}
