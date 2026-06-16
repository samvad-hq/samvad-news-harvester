package crawler

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/samvad-hq/samvad-news-harvester/internal/domain"
	"github.com/samvad-hq/samvad-news-harvester/pkg/providers"
	"github.com/samvad-hq/samvad-news-harvester/pkg/publishers"
)

// fakeFetcher returns preset articles or an error.
type fakeFetcher struct {
	id       string
	articles []domain.Article
	err      error
}

func (f *fakeFetcher) ID() string { return f.id }
func (f *fakeFetcher) Fetch(_ context.Context, _ providers.Provider) ([]domain.Article, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.articles, nil
}

// fakeRegistry maps provider type to a single fetcher.
type fakeRegistry struct {
	fetcher providers.Fetcher
}

func (f *fakeRegistry) FetcherFor(_ providers.Provider) (providers.Fetcher, error) {
	if f.fetcher == nil {
		return nil, errors.New("missing fetcher")
	}
	return f.fetcher, nil
}

// fakeScraper passes through or modifies titles.
type fakeScraper struct {
	prefix string
}

func (f fakeScraper) Enrich(_ context.Context, _ providers.Provider, articles []domain.Article) []domain.Article {
	out := make([]domain.Article, len(articles))
	for i, a := range articles {
		a.Title = f.prefix + a.Title
		out[i] = a
	}
	return out
}

// fakePublisher records published events and can inject errors.
type fakePublisher struct {
	mu        sync.Mutex
	events    []publishers.Event
	errOnID   string
	successes int
}

func (f *fakePublisher) Publish(_ context.Context, evt publishers.Event) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, evt)
	if evt.Article.ID == f.errOnID {
		return 0, errors.New("boom")
	}
	f.successes++
	return 1, nil
}

// fakeDeduper tracks seen IDs.
type fakeDeduper struct {
	mu      sync.Mutex
	seen    map[string]bool
	failID  string
	failErr error
}

func (f *fakeDeduper) SeenArticle(id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id == f.failID && f.failErr != nil {
		return false, f.failErr
	}
	return f.seen[id], nil
}

func (f *fakeDeduper) MarkArticle(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.seen == nil {
		f.seen = make(map[string]bool)
	}
	f.seen[id] = true
	return nil
}

func TestProviderProcessorPublishesFreshArticlesOnly(t *testing.T) {
	cfg := providers.Provider{ID: "p1", Name: "Provider1"}
	articles := []domain.Article{
		{ID: "a1", Title: "old"},
		{ID: "a2", Title: "new"},
	}

	deduper := &fakeDeduper{seen: map[string]bool{"a1": true}}
	pub := &fakePublisher{}

	processor := NewProviderProcessor(&fakeRegistry{
		fetcher: &fakeFetcher{id: "p1", articles: articles},
	}, fakeScraper{prefix: "enriched-"}, pub, nil, deduper)

	if err := processor.Process(context.Background(), cfg, 1); err != nil {
		t.Fatalf("Process: %v", err)
	}

	if len(pub.events) != 1 {
		t.Fatalf("expected 1 published event, got %d", len(pub.events))
	}
	evt := pub.events[0]
	if evt.Article.ID != "a2" || evt.Article.Title != "enriched-new" {
		t.Fatalf("unexpected article %+v", evt.Article)
	}
	if !deduper.seen["a2"] {
		t.Fatalf("MarkArticle not called for new article")
	}
}

func TestProviderProcessorAggregatesPublishErrors(t *testing.T) {
	cfg := providers.Provider{ID: "p1", Name: "Provider1"}
	pub := &fakePublisher{errOnID: "bad"}
	processor := NewProviderProcessor(&fakeRegistry{
		fetcher: &fakeFetcher{id: "p1", articles: []domain.Article{{ID: "bad"}}},
	}, nil, pub, nil, &fakeDeduper{})

	err := processor.Process(context.Background(), cfg, 0)
	if err == nil || !strings.Contains(err.Error(), "bad") {
		t.Fatalf("expected error mentioning bad article, got %v", err)
	}
}

func TestServiceRunAllCancelsEarly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	svc := NewService(&fakeRegistry{fetcher: &fakeFetcher{id: "p", articles: nil}}, nil, nil, nil)
	errs := svc.runAll(ctx, []providers.Provider{{ID: "p"}})
	if len(errs) != 0 {
		t.Fatalf("expected no errors on cancelled context, got %v", errs)
	}
}

func TestRunOnceLogsAndReturnsOnEmptyProviders(t *testing.T) {
	svc := NewService(&fakeRegistry{fetcher: &fakeFetcher{id: "p", articles: nil}}, nil, nil, nil)
	if err := svc.Run(context.Background(), nil); err == nil {
		t.Fatalf("expected error when providers list empty")
	}
}

func TestFilterNewArticlesHandlesDeduperErrors(t *testing.T) {
	deduper := &fakeDeduper{
		seen:    map[string]bool{"keep": false},
		failID:  "error",
		failErr: errors.New("lookup failed"),
	}
	processor := NewProviderProcessor(&fakeRegistry{fetcher: &fakeFetcher{id: "p"}}, nil, nil, nil, deduper)
	articles := []domain.Article{{ID: "keep"}, {ID: "skip"}, {ID: "error"}}
	deduper.seen["skip"] = true

	filtered := processor.filterNewArticles(providers.Provider{ID: "p"}, articles)
	if len(filtered) != 2 {
		t.Fatalf("expected 2 articles after filter, got %d", len(filtered))
	}
	if filtered[0].ID != "keep" || filtered[1].ID != "error" {
		t.Fatalf("unexpected filter result %#v", filtered)
	}
}
