// Package harvest runs the crawl loop.
//
// One pass over the source list is five stages per source: fetch the
// sitemap, identify the articles, ask which are new, enrich those, and
// deliver them. Sources run concurrently up to a configured limit; within
// a source the stages are sequential, because each depends on the last.
// The articles inside the enrich and deliver stages are themselves
// processed concurrently, under their own limits.
package harvest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/samvad-hq/samvad-news-harvester/internal/news"
	"github.com/samvad-hq/samvad-news-harvester/internal/sink"
	"github.com/samvad-hq/samvad-news-harvester/internal/source"
	"golang.org/x/sync/errgroup"
)

// Fetcher reads the articles a source is currently listing.
//
// Declared here, at the consumer. It is exported only because Deps is
// exported and its fields have to name it; nothing implements it outside
// internal/source.
type Fetcher interface {
	Fetch(ctx context.Context, src source.Config) ([]news.Article, error)
}

// Enricher fills in metadata a sitemap does not carry. It returns a slice
// of the same length and order as its input and cannot fail: an article
// whose page will not load is returned unchanged.
type Enricher interface {
	Enrich(ctx context.Context, src source.Config, arts []news.Article) []news.Article
}

// Deduper remembers which articles have already been delivered. Both
// methods take whole batches: a single source can carry thousands of IDs.
type Deduper interface {
	Unseen(ctx context.Context, ids []string) ([]string, error)
	Mark(ctx context.Context, ids []string) error
}

// Deps are everything the harvester needs.
type Deps struct {
	// Sources is the list to crawl, already validated.
	Sources []source.Config
	// Fetchers maps a source type to its fetcher.
	Fetchers map[string]Fetcher
	// Enricher fills in article metadata.
	Enricher Enricher
	// Deduper tracks delivered articles.
	Deduper Deduper
	// Sinks delivers events.
	Sinks *sink.Fanout
	// Log receives progress and failures.
	Log *slog.Logger
	// Interval is how often the full source list is crawled.
	Interval time.Duration
	// SourceConcurrency bounds how many sources are crawled at once.
	SourceConcurrency int
	// DeliveryConcurrency bounds how many of one source's articles are
	// delivered to the sinks at once.
	DeliveryConcurrency int
}

// Harvester crawls sources on an interval.
type Harvester struct {
	deps Deps
}

// New validates the dependencies and returns a harvester. Every source's
// type must have a registered fetcher, so a typo in the sources file
// fails at startup rather than on the first crawl.
func New(d Deps) (*Harvester, error) {
	var errs []error

	if len(d.Sources) == 0 {
		errs = append(errs, errors.New("harvest: no sources configured"))
	}
	if len(d.Fetchers) == 0 {
		errs = append(errs, errors.New("harvest: no fetchers registered"))
	}
	if d.Enricher == nil {
		errs = append(errs, errors.New("harvest: enricher is required"))
	}
	if d.Deduper == nil {
		errs = append(errs, errors.New("harvest: deduper is required"))
	}
	if d.Sinks == nil || d.Sinks.Len() == 0 {
		errs = append(errs, errors.New("harvest: at least one sink is required"))
	}
	if d.Log == nil {
		errs = append(errs, errors.New("harvest: logger is required"))
	}
	if d.Interval <= 0 {
		errs = append(errs, errors.New("harvest: interval must be positive"))
	}
	if d.SourceConcurrency < 1 {
		errs = append(errs, errors.New("harvest: source concurrency must be at least 1"))
	}
	if d.DeliveryConcurrency < 1 {
		errs = append(errs, errors.New("harvest: delivery concurrency must be at least 1"))
	}

	for _, src := range d.Sources {
		if _, ok := d.Fetchers[src.Type]; !ok {
			errs = append(errs, fmt.Errorf("harvest: source %s has type %q with no registered fetcher", src.ID, src.Type))
		}
	}

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return &Harvester{deps: d}, nil
}

// Run crawls immediately and then on every interval, until ctx is done.
//
// A cancelled context is a clean shutdown, so Run returns nil. Crawl
// failures are logged here and not returned, because the loop is expected
// to outlive them — this is the one place in the service that logs an
// error it received rather than returning it.
func (h *Harvester) Run(ctx context.Context) error {
	h.deps.Log.InfoContext(ctx, "harvester starting",
		"sources", len(h.deps.Sources),
		"sinks", h.deps.Sinks.Len(),
		"interval", h.deps.Interval.String(),
		"source_concurrency", h.deps.SourceConcurrency,
		"delivery_concurrency", h.deps.DeliveryConcurrency,
	)

	if err := h.RunOnce(ctx); err != nil && ctx.Err() == nil {
		h.deps.Log.ErrorContext(ctx, "crawl finished with errors", "error", err)
	}

	ticker := time.NewTicker(h.deps.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			h.deps.Log.InfoContext(ctx, "harvester stopping", "reason", context.Cause(ctx))
			return nil
		case <-ticker.C:
			if err := h.RunOnce(ctx); err != nil && ctx.Err() == nil {
				h.deps.Log.ErrorContext(ctx, "crawl finished with errors", "error", err)
			}
		}
	}
}

// RunOnce crawls every source once and returns the joined failures.
//
// One source failing does not stop the others: a publisher answering 403
// should not cost you the other twenty-five. errgroup.WithContext is
// deliberately not used for that reason — it would cancel siblings on the
// first error.
func (h *Harvester) RunOnce(ctx context.Context) error {
	start := time.Now()

	var (
		mu   sync.Mutex
		errs []error
	)

	var g errgroup.Group
	g.SetLimit(h.deps.SourceConcurrency)

	for _, src := range h.deps.Sources {
		if ctx.Err() != nil {
			break
		}
		g.Go(func() error {
			if err := h.runSource(ctx, src); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
			return nil
		})
	}
	// The goroutines never return an error, so Wait cannot fail.
	_ = g.Wait()

	h.deps.Log.InfoContext(ctx, "crawl complete",
		"sources", len(h.deps.Sources),
		"failed_sources", len(errs),
		"elapsed_ms", time.Since(start).Milliseconds(),
	)

	return errors.Join(errs...)
}

// FailedSources reports how many sources failed in the error RunOnce
// returned, so a caller can tell "some sources failed" from "every source
// failed" — the distinction cmd/harvester's -once flag needs to decide
// whether a cron wrapper should see a non-zero exit, given that three of
// the presently configured sources answer 403 by design and exiting
// non-zero on any failure would make the exit code permanently red. This
// is the smallest thing that exposes that distinction: RunOnce's return
// type does not change, and neither does what Run does with it. It works
// by relying on RunOnce joining exactly one wrapped error per failed
// source via errors.Join, whose result implements Unwrap() []error.
func FailedSources(err error) int {
	if err == nil {
		return 0
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		return len(joined.Unwrap())
	}
	return 1
}

// runSource takes one source through all five stages.
func (h *Harvester) runSource(ctx context.Context, src source.Config) error {
	start := time.Now()
	log := h.deps.Log.With("source", src.ID)

	articles, err := h.deps.Fetchers[src.Type].Fetch(ctx, src)
	if err != nil {
		return fmt.Errorf("source %s: %w", src.ID, err)
	}
	fetched := len(articles)
	if fetched == 0 {
		log.InfoContext(ctx, "source listed no articles")
		return nil
	}

	fresh, err := h.selectFresh(ctx, articles)
	if err != nil {
		return fmt.Errorf("source %s: dedupe: %w", src.ID, err)
	}
	if len(fresh) == 0 {
		log.InfoContext(ctx, "source crawled",
			"fetched", fetched, "fresh", 0, "delivered", 0,
			"elapsed_ms", time.Since(start).Milliseconds())
		return nil
	}

	fresh = h.deps.Enricher.Enrich(ctx, src, fresh)

	delivered, deliverErr := h.deliver(ctx, src, fresh)

	if len(delivered) > 0 {
		if err := h.deps.Deduper.Mark(ctx, delivered); err != nil {
			// Failing to mark means these articles are republished next
			// crawl. That is the safe direction, so it is reported and the
			// crawl continues.
			deliverErr = errors.Join(deliverErr, fmt.Errorf("mark delivered: %w", err))
		}
	}

	log.InfoContext(ctx, "source crawled",
		"fetched", fetched,
		"fresh", len(fresh),
		"delivered", len(delivered),
		"elapsed_ms", time.Since(start).Milliseconds(),
	)

	if deliverErr != nil {
		return fmt.Errorf("source %s: %w", src.ID, deliverErr)
	}
	return nil
}

// selectFresh asks the deduper which articles are new, in one call, and
// returns those articles in their original order.
func (h *Harvester) selectFresh(ctx context.Context, articles []news.Article) ([]news.Article, error) {
	ids := make([]string, 0, len(articles))
	for _, a := range articles {
		ids = append(ids, a.ID)
	}

	unseen, err := h.deps.Deduper.Unseen(ctx, ids)
	if err != nil {
		return nil, err
	}
	if len(unseen) == len(articles) {
		return articles, nil
	}

	keep := make(map[string]struct{}, len(unseen))
	for _, id := range unseen {
		keep[id] = struct{}{}
	}

	fresh := make([]news.Article, 0, len(unseen))
	for _, a := range articles {
		if _, ok := keep[a.ID]; ok {
			fresh = append(fresh, a)
		}
	}
	return fresh, nil
}

// deliver sends each article to every sink and returns the IDs that every
// sink accepted. An article that only some sinks accepted is deliberately
// not in that list: marking it would mean the failing sink never sees it
// again. Redelivering to the sinks that succeeded is the cheaper mistake.
//
// Articles are delivered concurrently, up to DeliveryConcurrency at once.
// Sinks are required to be safe for concurrent use, and this is what lets
// a batching sink such as Pub/Sub see more than one message at a time.
// The returned IDs stay in article order regardless of the order in which
// deliveries complete, because each result is written to its own slot
// rather than appended as it finishes.
//
// errgroup.WithContext is deliberately not used, for the same reason
// RunOnce avoids it: one article's sink failure must not cancel the
// deliveries already in flight beside it.
//
// The returned error is a count, not a join of every article's failure:
// Fanout.Send already logs the per-sink detail at the point of failure, so
// joining it again here would, under a sustained outage, repeat that same
// detail once per article into a single log line with no cap on its
// length. A summary is everything runSource's caller needs to know that
// this source had a problem.
func (h *Harvester) deliver(ctx context.Context, src source.Config, articles []news.Article) ([]string, error) {
	accepted := make([]bool, len(articles))
	attempted := 0

	var g errgroup.Group
	g.SetLimit(h.deps.DeliveryConcurrency)

	for i, art := range articles {
		if ctx.Err() != nil {
			break
		}
		attempted++
		g.Go(func() error {
			accepted[i] = h.deps.Sinks.Send(ctx, news.NewEvent(src.ID, src.Name, art)).OK()
			return nil
		})
	}
	// The goroutines never return an error, so Wait cannot fail.
	_ = g.Wait()

	// Only what was actually dispatched is judged. A context cancelled
	// part-way leaves the rest of the batch unattempted, and reporting
	// those as delivery failures would turn every shutdown into a crawl
	// error.
	delivered := make([]string, 0, attempted)
	failed := 0
	for i, art := range articles[:attempted] {
		if accepted[i] {
			delivered = append(delivered, art.ID)
			continue
		}
		failed++
	}

	if failed > 0 {
		return delivered, fmt.Errorf("%d of %d articles failed delivery", failed, len(articles))
	}
	return delivered, nil
}
