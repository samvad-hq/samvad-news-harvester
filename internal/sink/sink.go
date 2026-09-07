// Package sink delivers events to their destinations.
//
// It is called sink rather than publisher because this project is about
// news publishers: calling an SQS queue a publisher too made "publisher"
// mean two different things in one codebase.
//
// Delivery is at-least-once. Fanout reports which sinks accepted an event
// and which did not, and the caller records the article as seen only when
// every sink accepted. A partial failure therefore replays the event to
// the sinks that already succeeded on the next crawl, which is the right
// trade against silently losing it. Consumers must key on Article.ID.
package sink

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/samvad-hq/samvad-news-harvester/internal/news"
)

// Sink delivers one event to one destination.
//
// This is the only exported interface in the service, because it is the
// only abstraction with several implementations chosen at runtime from
// configuration.
type Sink interface {
	// Name identifies the sink in logs and in a Result.
	Name() string
	// Send delivers one event. It must be safe for concurrent use.
	Send(ctx context.Context, evt news.Event) error
}

// Result reports the outcome of fanning one event out.
type Result struct {
	// Delivered names the sinks that accepted the event.
	Delivered []string
	// Failed maps a sink name to the error it returned.
	Failed map[string]error
}

// OK reports whether every configured sink accepted the event. Only then
// may the article be recorded as delivered.
//
// A fan-out with no sinks is not OK: delivering to nothing is not delivery.
func (r Result) OK() bool {
	return len(r.Failed) == 0 && len(r.Delivered) > 0
}

// Err joins the failures, or returns nil when there were none. It is for
// reporting the failures, not for deciding delivery: a fan-out with no
// sinks has no failures and so returns nil here, yet is still not a
// delivery. Callers must gate on OK, never on Err returning non-nil.
func (r Result) Err() error {
	if len(r.Failed) == 0 {
		return nil
	}
	errs := make([]error, 0, len(r.Failed))
	for name, err := range r.Failed {
		errs = append(errs, fmt.Errorf("sink %s: %w", name, err))
	}
	return errors.Join(errs...)
}

// Fanout delivers each event to every sink.
type Fanout struct {
	sinks []Sink
	log   *slog.Logger
}

// NewFanout returns a fan-out over the given sinks, ignoring nil entries.
func NewFanout(sinks []Sink, log *slog.Logger) *Fanout {
	kept := make([]Sink, 0, len(sinks))
	for _, s := range sinks {
		if s != nil {
			kept = append(kept, s)
		}
	}
	return &Fanout{sinks: kept, log: log}
}

// Len returns the number of active sinks.
func (f *Fanout) Len() int { return len(f.sinks) }

// Send delivers evt to every sink concurrently and reports the outcome per
// sink. It does not return an error: a partial failure is a result the
// caller must act on, not an exception.
func (f *Fanout) Send(ctx context.Context, evt news.Event) Result {
	res := Result{Delivered: make([]string, 0, len(f.sinks))}
	if len(f.sinks) == 0 {
		return res
	}

	type outcome struct {
		name string
		err  error
	}
	results := make([]outcome, len(f.sinks))

	var wg sync.WaitGroup
	for i, s := range f.sinks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = outcome{name: s.Name(), err: s.Send(ctx, evt)}
		}()
	}
	wg.Wait()

	for _, out := range results {
		if out.err == nil {
			res.Delivered = append(res.Delivered, out.name)
			continue
		}
		if res.Failed == nil {
			res.Failed = make(map[string]error, len(f.sinks))
		}
		res.Failed[out.name] = out.err
		f.log.WarnContext(ctx, "sink rejected event",
			"sink", out.name,
			"source_id", evt.SourceID,
			"article_id", evt.Article.ID,
			"error", out.err)
	}
	return res
}
