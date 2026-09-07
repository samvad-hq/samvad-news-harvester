package harvest_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/samvad-hq/samvad-news-harvester/internal/harvest"
	"github.com/samvad-hq/samvad-news-harvester/internal/news"
	"github.com/samvad-hq/samvad-news-harvester/internal/sink"
	"github.com/samvad-hq/samvad-news-harvester/internal/source"
	"github.com/stretchr/testify/require"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// --- fakes ------------------------------------------------------------

type fakeFetcher struct {
	articles map[string][]news.Article // by source ID
	err      error
	calls    atomic.Int64
}

func (f *fakeFetcher) Fetch(_ context.Context, src source.Config) ([]news.Article, error) {
	f.calls.Add(1)
	if f.err != nil {
		return nil, f.err
	}
	return f.articles[src.ID], nil
}

type fakeEnricher struct{ calls atomic.Int64 }

func (f *fakeEnricher) Enrich(_ context.Context, _ source.Config, arts []news.Article) []news.Article {
	f.calls.Add(1)
	out := make([]news.Article, len(arts))
	for i, a := range arts {
		a.Description = "enriched"
		out[i] = a
	}
	return out
}

type fakeDeduper struct {
	mu     sync.Mutex
	seen   map[string]bool
	marked []string
	err    error
}

func newFakeDeduper() *fakeDeduper { return &fakeDeduper{seen: map[string]bool{}} }

func (f *fakeDeduper) Unseen(_ context.Context, ids []string) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !f.seen[id] {
			out = append(out, id)
		}
	}
	return out, nil
}

func (f *fakeDeduper) Mark(_ context.Context, ids []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.marked = append(f.marked, ids...)
	for _, id := range ids {
		f.seen[id] = true
	}
	return nil
}

func (f *fakeDeduper) markedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.marked...)
}

type recordingSink struct {
	name string
	err  error
	mu   sync.Mutex
	got  []news.Event
}

func (r *recordingSink) Name() string { return r.name }

func (r *recordingSink) Send(_ context.Context, evt news.Event) error {
	if r.err != nil {
		return r.err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, evt)
	return nil
}

func (r *recordingSink) events() []news.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]news.Event(nil), r.got...)
}

// --- helpers ----------------------------------------------------------

func testSources(ids ...string) []source.Config {
	out := make([]source.Config, 0, len(ids))
	for _, id := range ids {
		out = append(out, source.Config{
			ID: id, Name: id, Type: source.TypeNewsSitemap,
			URL: "https://" + id + ".example/s.xml",
		})
	}
	return out
}

func article(id string) news.Article {
	return news.Article{ID: id, URL: "https://pub.example/" + id, Title: id}
}

// --- tests ------------------------------------------------------------

func TestRunOnceDeliversAndMarks(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFetcher{articles: map[string][]news.Article{
		"a": {article("1"), article("2")},
	}}
	dedup := newFakeDeduper()
	out := &recordingSink{name: "out"}

	h, err := harvest.New(harvest.Deps{
		Sources:             testSources("a"),
		Fetchers:            map[string]harvest.Fetcher{source.TypeNewsSitemap: fetcher},
		Enricher:            &fakeEnricher{},
		Deduper:             dedup,
		Sinks:               sink.NewFanout([]sink.Sink{out}, discardLogger()),
		Log:                 discardLogger(),
		Interval:            time.Hour,
		SourceConcurrency:   4,
		DeliveryConcurrency: 4,
	})
	require.NoError(t, err)

	require.NoError(t, h.RunOnce(context.Background()))

	require.Len(t, out.events(), 2)
	require.ElementsMatch(t, []string{"1", "2"}, dedup.markedIDs())
	require.Equal(t, "enriched", out.events()[0].Article.Description)
	require.Equal(t, "a", out.events()[0].SourceID)
}

func TestRunOnceSkipsArticlesAlreadySeen(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFetcher{articles: map[string][]news.Article{"a": {article("1"), article("2")}}}
	dedup := newFakeDeduper()
	dedup.seen["1"] = true
	enricher := &fakeEnricher{}
	out := &recordingSink{name: "out"}

	h, err := harvest.New(harvest.Deps{
		Sources:             testSources("a"),
		Fetchers:            map[string]harvest.Fetcher{source.TypeNewsSitemap: fetcher},
		Enricher:            enricher,
		Deduper:             dedup,
		Sinks:               sink.NewFanout([]sink.Sink{out}, discardLogger()),
		Log:                 discardLogger(),
		Interval:            time.Hour,
		SourceConcurrency:   4,
		DeliveryConcurrency: 4,
	})
	require.NoError(t, err)
	require.NoError(t, h.RunOnce(context.Background()))

	require.Len(t, out.events(), 1)
	require.Equal(t, "2", out.events()[0].Article.ID)
	require.Equal(t, []string{"2"}, dedup.markedIDs())
}

// The other half of the C2 fix. When any sink rejects the event, the
// article must not be marked, so the next crawl retries it.
func TestArticleIsNotMarkedWhenASinkFails(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFetcher{articles: map[string][]news.Article{"a": {article("1")}}}
	dedup := newFakeDeduper()
	good := &recordingSink{name: "good"}
	bad := &recordingSink{name: "bad", err: errors.New("webhook down")}

	h, err := harvest.New(harvest.Deps{
		Sources:             testSources("a"),
		Fetchers:            map[string]harvest.Fetcher{source.TypeNewsSitemap: fetcher},
		Enricher:            &fakeEnricher{},
		Deduper:             dedup,
		Sinks:               sink.NewFanout([]sink.Sink{good, bad}, discardLogger()),
		Log:                 discardLogger(),
		Interval:            time.Hour,
		SourceConcurrency:   4,
		DeliveryConcurrency: 4,
	})
	require.NoError(t, err)

	err = h.RunOnce(context.Background())
	require.Error(t, err, "a failed delivery is reported")
	require.Empty(t, dedup.markedIDs(), "a partially delivered article must stay unseen")
	require.Len(t, good.events(), 1, "the healthy sink still received it")
}

func TestOneFailingSourceDoesNotStopTheOthers(t *testing.T) {
	t.Parallel()

	fetcher := &failOneFetcher{
		fail: "b",
		articles: map[string][]news.Article{
			"a": {article("1")},
			"c": {article("3")},
		},
	}
	dedup := newFakeDeduper()
	out := &recordingSink{name: "out"}

	h, err := harvest.New(harvest.Deps{
		Sources:             testSources("a", "b", "c"),
		Fetchers:            map[string]harvest.Fetcher{source.TypeNewsSitemap: fetcher},
		Enricher:            &fakeEnricher{},
		Deduper:             dedup,
		Sinks:               sink.NewFanout([]sink.Sink{out}, discardLogger()),
		Log:                 discardLogger(),
		Interval:            time.Hour,
		SourceConcurrency:   4,
		DeliveryConcurrency: 4,
	})
	require.NoError(t, err)

	err = h.RunOnce(context.Background())
	require.ErrorContains(t, err, "b")
	require.Len(t, out.events(), 2, "a and c still delivered")
}

type failOneFetcher struct {
	fail     string
	articles map[string][]news.Article
}

func (f *failOneFetcher) Fetch(_ context.Context, src source.Config) ([]news.Article, error) {
	if src.ID == f.fail {
		return nil, errors.New("sitemap returned 403")
	}
	return f.articles[src.ID], nil
}

func TestRunOnceRespectsTheConcurrencyLimit(t *testing.T) {
	t.Parallel()

	counter := &concurrencyFetcher{}
	h, err := harvest.New(harvest.Deps{
		Sources:             testSources("a", "b", "c", "d", "e", "f", "g", "h"),
		Fetchers:            map[string]harvest.Fetcher{source.TypeNewsSitemap: counter},
		Enricher:            &fakeEnricher{},
		Deduper:             newFakeDeduper(),
		Sinks:               sink.NewFanout([]sink.Sink{&recordingSink{name: "out"}}, discardLogger()),
		Log:                 discardLogger(),
		Interval:            time.Hour,
		SourceConcurrency:   2,
		DeliveryConcurrency: 2,
	})
	require.NoError(t, err)
	require.NoError(t, h.RunOnce(context.Background()))

	require.LessOrEqual(t, counter.peak.Load(), int64(2))
}

type concurrencyFetcher struct {
	inFlight atomic.Int64
	peak     atomic.Int64
}

func (c *concurrencyFetcher) Fetch(context.Context, source.Config) ([]news.Article, error) {
	n := c.inFlight.Add(1)
	for {
		peak := c.peak.Load()
		if n <= peak || c.peak.CompareAndSwap(peak, n) {
			break
		}
	}
	time.Sleep(20 * time.Millisecond)
	c.inFlight.Add(-1)
	return nil, nil
}

// The C1 regression test. The old pipeline could block forever when
// cancellation landed between the producer's ctx check and its unbuffered
// send, because every worker had already returned.
func TestRunOnceReturnsPromptlyOnCancellation(t *testing.T) {
	t.Parallel()

	block := make(chan struct{})
	h, err := harvest.New(harvest.Deps{
		Sources:             testSources("a", "b", "c", "d", "e", "f", "g", "h", "i", "j"),
		Fetchers:            map[string]harvest.Fetcher{source.TypeNewsSitemap: &blockingFetcher{block: block}},
		Enricher:            &fakeEnricher{},
		Deduper:             newFakeDeduper(),
		Sinks:               sink.NewFanout([]sink.Sink{&recordingSink{name: "out"}}, discardLogger()),
		Log:                 discardLogger(),
		Interval:            time.Hour,
		SourceConcurrency:   2,
		DeliveryConcurrency: 2,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- h.RunOnce(ctx) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		close(block)
		t.Fatal("RunOnce deadlocked on cancellation")
	}
	close(block)
}

type blockingFetcher struct{ block chan struct{} }

func (b *blockingFetcher) Fetch(ctx context.Context, _ source.Config) ([]news.Article, error) {
	select {
	case <-b.block:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestRunCrawlsImmediatelyThenOnTheInterval(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFetcher{articles: map[string][]news.Article{}}
	h, err := harvest.New(harvest.Deps{
		Sources:             testSources("a"),
		Fetchers:            map[string]harvest.Fetcher{source.TypeNewsSitemap: fetcher},
		Enricher:            &fakeEnricher{},
		Deduper:             newFakeDeduper(),
		Sinks:               sink.NewFanout([]sink.Sink{&recordingSink{name: "out"}}, discardLogger()),
		Log:                 discardLogger(),
		Interval:            30 * time.Millisecond,
		SourceConcurrency:   2,
		DeliveryConcurrency: 2,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	require.NoError(t, h.Run(ctx), "a cancelled context is a clean shutdown, not an error")
	require.GreaterOrEqual(t, fetcher.calls.Load(), int64(2), "one immediate crawl plus at least one tick")
}

// TestFailedSourcesCountsWrappedErrors pins the exit-code contract in
// finding #6: cmd/harvester's -once flag needs to tell "some sources
// failed" from "every source failed" without RunOnce's return type
// changing, and FailedSources is what makes that distinction from the
// joined error RunOnce already returns.
func TestFailedSourcesCountsWrappedErrors(t *testing.T) {
	t.Parallel()

	require.Equal(t, 0, harvest.FailedSources(nil))

	one := fmt.Errorf("source a: %w", errors.New("boom"))
	require.Equal(t, 1, harvest.FailedSources(one))

	joined := errors.Join(
		fmt.Errorf("source a: %w", errors.New("boom")),
		fmt.Errorf("source b: %w", errors.New("boom")),
	)
	require.Equal(t, 2, harvest.FailedSources(joined))
}

func TestDeliverReportsASummaryNotOneErrorPerArticle(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFetcher{articles: map[string][]news.Article{"a": {article("1"), article("2"), article("3")}}}
	dedup := newFakeDeduper()
	bad := &recordingSink{name: "bad", err: errors.New("webhook down")}

	h, err := harvest.New(harvest.Deps{
		Sources:             testSources("a"),
		Fetchers:            map[string]harvest.Fetcher{source.TypeNewsSitemap: fetcher},
		Enricher:            &fakeEnricher{},
		Deduper:             dedup,
		Sinks:               sink.NewFanout([]sink.Sink{bad}, discardLogger()),
		Log:                 discardLogger(),
		Interval:            time.Hour,
		SourceConcurrency:   4,
		DeliveryConcurrency: 4,
	})
	require.NoError(t, err)

	err = h.RunOnce(context.Background())
	require.Error(t, err)
	require.ErrorContains(t, err, "3 of 3 articles failed delivery",
		"the error is a per-source summary, not one joined error per article")
}

func TestRunRejectsAnUnknownSourceType(t *testing.T) {
	t.Parallel()

	sources := testSources("a")
	sources[0].Type = "rss"

	_, err := harvest.New(harvest.Deps{
		Sources:             sources,
		Fetchers:            map[string]harvest.Fetcher{source.TypeNewsSitemap: &fakeFetcher{}},
		Enricher:            &fakeEnricher{},
		Deduper:             newFakeDeduper(),
		Sinks:               sink.NewFanout([]sink.Sink{&recordingSink{name: "out"}}, discardLogger()),
		Log:                 discardLogger(),
		Interval:            time.Hour,
		SourceConcurrency:   2,
		DeliveryConcurrency: 2,
	})
	require.ErrorContains(t, err, "rss")
}

func TestNewRejectsMissingDependencies(t *testing.T) {
	t.Parallel()

	base := func() harvest.Deps {
		return harvest.Deps{
			Sources:             testSources("a"),
			Fetchers:            map[string]harvest.Fetcher{source.TypeNewsSitemap: &fakeFetcher{}},
			Enricher:            &fakeEnricher{},
			Deduper:             newFakeDeduper(),
			Sinks:               sink.NewFanout([]sink.Sink{&recordingSink{name: "out"}}, discardLogger()),
			Log:                 discardLogger(),
			Interval:            time.Hour,
			SourceConcurrency:   2,
			DeliveryConcurrency: 2,
		}
	}

	t.Run("no sources", func(t *testing.T) {
		d := base()
		d.Sources = nil
		_, err := harvest.New(d)
		require.Error(t, err)
	})

	t.Run("no deduper", func(t *testing.T) {
		d := base()
		d.Deduper = nil
		_, err := harvest.New(d)
		require.Error(t, err)
	})

	t.Run("no interval", func(t *testing.T) {
		d := base()
		d.Interval = 0
		_, err := harvest.New(d)
		require.Error(t, err)
	})
}

// TestDeliveryIsConcurrentWithinASource is the gate on the change that
// made delivery parallel. barrierSink refuses to return until as many
// Send calls are in flight at once as the source has articles, so a
// sequential deliver loop cannot satisfy it: the first call would wait
// for a second that is never made, time out, and fail the delivery.
func TestDeliveryIsConcurrentWithinASource(t *testing.T) {
	t.Parallel()

	const articles = 3

	fetcher := &fakeFetcher{articles: map[string][]news.Article{
		"a": {article("1"), article("2"), article("3")},
	}}
	dedup := newFakeDeduper()
	barrier := newBarrierSink("barrier", articles)

	h, err := harvest.New(harvest.Deps{
		Sources:             testSources("a"),
		Fetchers:            map[string]harvest.Fetcher{source.TypeNewsSitemap: fetcher},
		Enricher:            &fakeEnricher{},
		Deduper:             dedup,
		Sinks:               sink.NewFanout([]sink.Sink{barrier}, discardLogger()),
		Log:                 discardLogger(),
		Interval:            time.Hour,
		SourceConcurrency:   1,
		DeliveryConcurrency: articles,
	})
	require.NoError(t, err)

	require.NoError(t, h.RunOnce(context.Background()),
		"every article must be in flight at once for the barrier to release")
	require.Len(t, dedup.markedIDs(), articles)
}

// barrierSink blocks every Send until want of them are in flight
// simultaneously, then releases them all. A caller that delivers one
// article at a time never reaches want and every Send fails on the
// timeout instead.
type barrierSink struct {
	name    string
	want    int
	mu      sync.Mutex
	arrived int
	release chan struct{}
}

func newBarrierSink(name string, want int) *barrierSink {
	return &barrierSink{name: name, want: want, release: make(chan struct{})}
}

func (b *barrierSink) Name() string { return b.name }

func (b *barrierSink) Send(context.Context, news.Event) error {
	b.mu.Lock()
	b.arrived++
	last := b.arrived == b.want
	b.mu.Unlock()

	if last {
		close(b.release)
	}
	select {
	case <-b.release:
		return nil
	case <-time.After(2 * time.Second):
		return errors.New("delivery never became concurrent")
	}
}

// TestDeliveryRespectsTheConcurrencyLimit pins both halves of the bound:
// no more than DeliveryConcurrency sends are ever in flight for one
// source, and the limit is actually reached rather than the loop having
// silently stayed sequential.
func TestDeliveryRespectsTheConcurrencyLimit(t *testing.T) {
	t.Parallel()

	arts := make([]news.Article, 0, 8)
	for i := range 8 {
		arts = append(arts, article(fmt.Sprintf("%d", i)))
	}

	counter := &concurrencySink{name: "counter"}
	h, err := harvest.New(harvest.Deps{
		Sources:             testSources("a"),
		Fetchers:            map[string]harvest.Fetcher{source.TypeNewsSitemap: &fakeFetcher{articles: map[string][]news.Article{"a": arts}}},
		Enricher:            &fakeEnricher{},
		Deduper:             newFakeDeduper(),
		Sinks:               sink.NewFanout([]sink.Sink{counter}, discardLogger()),
		Log:                 discardLogger(),
		Interval:            time.Hour,
		SourceConcurrency:   1,
		DeliveryConcurrency: 2,
	})
	require.NoError(t, err)
	require.NoError(t, h.RunOnce(context.Background()))

	require.LessOrEqual(t, counter.peak.Load(), int64(2), "the limit is never exceeded")
	require.Equal(t, int64(2), counter.peak.Load(), "the limit is actually used")
}

type concurrencySink struct {
	name     string
	inFlight atomic.Int64
	peak     atomic.Int64
}

func (c *concurrencySink) Name() string { return c.name }

func (c *concurrencySink) Send(context.Context, news.Event) error {
	n := c.inFlight.Add(1)
	for {
		peak := c.peak.Load()
		if n <= peak || c.peak.CompareAndSwap(peak, n) {
			break
		}
	}
	time.Sleep(20 * time.Millisecond)
	c.inFlight.Add(-1)
	return nil
}

// TestDeliveredIDsKeepArticleOrder guards the one property concurrency
// could quietly take away. reverseDelaySink finishes the last article
// first, so an implementation that appended IDs as deliveries completed
// would hand Mark a reversed batch.
func TestDeliveredIDsKeepArticleOrder(t *testing.T) {
	t.Parallel()

	arts := []news.Article{article("1"), article("2"), article("3"), article("4")}
	dedup := newFakeDeduper()

	h, err := harvest.New(harvest.Deps{
		Sources:             testSources("a"),
		Fetchers:            map[string]harvest.Fetcher{source.TypeNewsSitemap: &fakeFetcher{articles: map[string][]news.Article{"a": arts}}},
		Enricher:            &fakeEnricher{},
		Deduper:             dedup,
		Sinks:               sink.NewFanout([]sink.Sink{&reverseDelaySink{name: "slow", ids: []string{"4", "3", "2", "1"}}}, discardLogger()),
		Log:                 discardLogger(),
		Interval:            time.Hour,
		SourceConcurrency:   1,
		DeliveryConcurrency: 4,
	})
	require.NoError(t, err)
	require.NoError(t, h.RunOnce(context.Background()))

	require.Equal(t, []string{"1", "2", "3", "4"}, dedup.markedIDs(),
		"Mark receives the batch in article order regardless of completion order")
}

// reverseDelaySink delays each article by its position in ids, so the
// article listed first there completes last.
type reverseDelaySink struct {
	name string
	ids  []string
}

func (r *reverseDelaySink) Name() string { return r.name }

func (r *reverseDelaySink) Send(_ context.Context, evt news.Event) error {
	for i, id := range r.ids {
		if id == evt.Article.ID {
			time.Sleep(time.Duration(i+1) * 20 * time.Millisecond)
			break
		}
	}
	return nil
}
