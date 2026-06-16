package publishers

import (
	"context"
	"errors"
	"testing"
)

type stubPublisher struct {
	id    string
	typ   string
	err   error
	calls int
}

func (s *stubPublisher) ID() string   { return s.id }
func (s *stubPublisher) Type() string { return s.typ }
func (s *stubPublisher) Publish(context.Context, Event) error {
	s.calls++
	return s.err
}

func TestFanoutPublishAggregatesErrors(t *testing.T) {
	fanout := NewFanout([]Publisher{
		&stubPublisher{id: "ok", typ: "http"},
		&stubPublisher{id: "bad", typ: "http", err: errors.New("failed")},
	})

	count, err := fanout.Publish(context.Background(), Event{})
	if count != 1 {
		t.Fatalf("expected 1 success, got %d", count)
	}
	if err == nil {
		t.Fatalf("expected aggregated error")
	}
}

func TestBuildAllWithDefaultRegistry(t *testing.T) {
	reg := DefaultRegistry()
	pubs, err := BuildAll(context.Background(), reg, []PublisherConfig{
		{ID: "http", Type: TypeHTTP, HTTP: &HTTPPublisherConfig{URL: "https://example.com"}},
	}, nil)
	if err != nil {
		t.Fatalf("BuildAll: %v", err)
	}
	if len(pubs) != 1 {
		t.Fatalf("expected 1 publisher, got %d", len(pubs))
	}
}
