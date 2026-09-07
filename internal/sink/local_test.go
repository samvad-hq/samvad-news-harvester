package sink_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/samvad-hq/samvad-news-harvester/internal/news"
	"github.com/samvad-hq/samvad-news-harvester/internal/sink"
	"github.com/stretchr/testify/require"
)

func TestLogSinkWritesTheEvent(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	s := sink.NewLog("local-log", sink.LogConfig{Level: "info"}, logger)
	require.Equal(t, "local-log", s.Name())

	evt := news.NewEvent("thehindu", "The Hindu", news.Article{
		ID:    "abc",
		Title: "Headline",
		URL:   "https://www.thehindu.com/story",
	})
	require.NoError(t, s.Send(context.Background(), evt))

	var line map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line))
	require.Equal(t, "article", line["msg"])
	require.Equal(t, "thehindu", line["source_id"])
	require.Equal(t, "abc", line["article_id"])
	require.Equal(t, "https://www.thehindu.com/story", line["url"])
}

func TestHTTPSinkPostsTheEvent(t *testing.T) {
	t.Parallel()

	var got news.Event
	var gotAuth, gotMethod, gotContentType string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	s, err := sink.NewHTTP("webhook", sink.HTTPConfig{
		URL:     srv.URL,
		Method:  "POST",
		Timeout: 5 * time.Second,
		Headers: map[string]string{"Authorization": "Bearer secret"},
	})
	require.NoError(t, err)
	require.Equal(t, "webhook", s.Name())

	evt := news.NewEvent("thehindu", "The Hindu", news.Article{ID: "abc"})
	require.NoError(t, s.Send(context.Background(), evt))

	require.Equal(t, "POST", gotMethod)
	require.Equal(t, "Bearer secret", gotAuth)
	require.Equal(t, "application/json", gotContentType)
	require.Equal(t, "abc", got.Article.ID)
}

func TestHTTPSinkReportsAnErrorStatus(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("upstream exploded"))
	}))
	defer srv.Close()

	s, err := sink.NewHTTP("webhook", sink.HTTPConfig{URL: srv.URL, Method: "POST", Timeout: 5 * time.Second})
	require.NoError(t, err)

	err = s.Send(context.Background(), news.NewEvent("s", "S", news.Article{ID: "x"}))
	require.ErrorContains(t, err, "500")
	require.ErrorContains(t, err, "upstream exploded")
}

// TestHTTPSinkErrorDoesNotLeakTheWebhookSecretPath pins the fix for
// finding #1: for a Slack/Discord/Teams-style webhook the URL path IS the
// bearer credential, so a transport failure must never echo it. The error
// must still name the host, which is what an operator needs to tell one
// failing webhook from another.
func TestHTTPSinkErrorDoesNotLeakTheWebhookSecretPath(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	host := strings.TrimPrefix(srv.URL, "http://")
	const secretPath = "/services/T00000000/B00000000/xxxxxxxxxxxxxxxxxxxxxxxx"
	webhookURL := srv.URL + secretPath
	srv.Close() // the address now refuses connections, forcing a transport error

	s, err := sink.NewHTTP("webhook", sink.HTTPConfig{URL: webhookURL, Method: "POST", Timeout: 2 * time.Second})
	require.NoError(t, err)

	sendErr := s.Send(context.Background(), news.NewEvent("s", "S", news.Article{ID: "x"}))
	require.Error(t, sendErr)
	require.NotContains(t, sendErr.Error(), "T00000000")
	require.NotContains(t, sendErr.Error(), secretPath)
	require.Contains(t, sendErr.Error(), host)
}

func TestHTTPSinkHonoursCancellation(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
	}))
	defer func() { close(release); srv.Close() }()

	s, err := sink.NewHTTP("webhook", sink.HTTPConfig{URL: srv.URL, Method: "POST", Timeout: 5 * time.Second})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.ErrorIs(t, s.Send(ctx, news.NewEvent("s", "S", news.Article{ID: "x"})), context.Canceled)
}
