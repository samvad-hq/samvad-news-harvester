package sink

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// Build turns configuration into sinks, skipping the disabled ones.
//
// The returned function closes every sink that holds a connection, and
// must be called on shutdown. Build replaces the old builder registry: a
// map of type to constructor plus a mutex bought nothing, because it was
// populated once at startup and never written again.
func Build(ctx context.Context, cfgs []Config, log *slog.Logger) ([]Sink, func() error, error) {
	var (
		sinks   []Sink
		closers []func() error
	)

	// closeAll runs every closer collected so far. Only PubSub returns a
	// non-nil closer today, and PubSub cannot be constructed in a unit
	// test (it dials a real client), so the claim that closers actually
	// run on the partial-failure path is verified by inspection here, not
	// by a test: sinks and closers grow together in the loop below, so a
	// failure at the Nth sink closes everything built before it. What the
	// tests in build_test.go do cover is the surrounding shape — that
	// Build reaches this function on error and returns cleanly rather
	// than panicking, with closers empty in every case they exercise.
	closeAll := func() error {
		var errs []error
		for _, closeFn := range closers {
			if err := closeFn(); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}

	for _, cfg := range cfgs {
		if !cfg.IsEnabled() {
			log.Debug("sink is disabled, skipping", "sink", cfg.ID, "type", cfg.Type)
			continue
		}

		built, closeFn, err := build(ctx, cfg, log)
		if err != nil {
			_ = closeAll()
			return nil, nil, err
		}
		sinks = append(sinks, built)
		if closeFn != nil {
			closers = append(closers, closeFn)
		}
	}

	if len(sinks) == 0 {
		_ = closeAll()
		return nil, nil, errors.New("sink: no enabled sinks configured")
	}
	return sinks, closeAll, nil
}

// build constructs one sink and, where it holds a connection, the function
// that releases it.
//
// It dereferences cfg.HTTP, cfg.SQS, cfg.SNS and cfg.PubSub without a nil
// check because Config.Validate has already rejected a config whose block
// is missing, and LoadFile validates every entry including disabled ones.
func build(ctx context.Context, cfg Config, log *slog.Logger) (Sink, func() error, error) {
	switch cfg.Type {
	case TypeLog:
		logCfg := LogConfig{}
		if cfg.Log != nil {
			logCfg = *cfg.Log
		}
		return NewLog(cfg.ID, logCfg, log), nil, nil

	case TypeHTTP:
		s, err := NewHTTP(cfg.ID, *cfg.HTTP)
		if err != nil {
			return nil, nil, err
		}
		return s, nil, nil

	case TypeSQS:
		s, err := NewSQS(ctx, cfg.ID, *cfg.SQS)
		if err != nil {
			return nil, nil, err
		}
		return s, nil, nil

	case TypeSNS:
		s, err := NewSNS(ctx, cfg.ID, *cfg.SNS)
		if err != nil {
			return nil, nil, err
		}
		return s, nil, nil

	case TypePubSub:
		s, err := NewPubSub(ctx, cfg.ID, *cfg.PubSub)
		if err != nil {
			return nil, nil, err
		}
		return s, s.Close, nil

	default:
		return nil, nil, fmt.Errorf("sink %s: unsupported type %q", cfg.ID, cfg.Type)
	}
}
