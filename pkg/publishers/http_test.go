package publishers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPPublisherSuccess(t *testing.T) {
	var received bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if got := r.Header.Get("X-Test"); got != "1" {
			t.Fatalf("missing header, got %s", got)
		}
		received = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	pub, err := newHTTPPublisher(context.Background(), PublisherConfig{
		ID:   "hook",
		Type: TypeHTTP,
		HTTP: &HTTPPublisherConfig{
			URL:            srv.URL,
			Method:         http.MethodPost,
			Headers:        map[string]string{"X-Test": "1"},
			TimeoutSeconds: 2,
		},
	}, nil)
	if err != nil {
		t.Fatalf("newHTTPPublisher: %v", err)
	}

	if err := pub.Publish(context.Background(), Event{}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if !received {
		t.Fatalf("server did not receive request")
	}
}

func TestHTTPPublisherErrorOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusBadRequest)
	}))
	defer srv.Close()

	pub, err := newHTTPPublisher(context.Background(), PublisherConfig{
		ID:   "hook",
		Type: TypeHTTP,
		HTTP: &HTTPPublisherConfig{
			URL:            srv.URL,
			Method:         http.MethodPost,
			TimeoutSeconds: 1,
		},
	}, nil)
	if err != nil {
		t.Fatalf("newHTTPPublisher: %v", err)
	}

	if err := pub.Publish(context.Background(), Event{}); err == nil {
		t.Fatalf("expected error on non-2xx response")
	}
}
