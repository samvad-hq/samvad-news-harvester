package httpx_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/samvad-hq/samvad-news-harvester/internal/httpx"
	"github.com/stretchr/testify/require"
)

func TestGetReturnsBodyStatusAndValidators(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "test-agent", r.Header.Get("User-Agent"))
		require.Equal(t, "application/xml", r.Header.Get("Accept"))
		w.Header().Set("ETag", `"abc123"`)
		w.Header().Set("Last-Modified", "Sun, 06 Sep 2026 04:00:00 GMT")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<urlset></urlset>"))
	}))
	defer srv.Close()

	c, err := httpx.New(5*time.Second, httpx.WithPrivateNetworksAllowed())
	require.NoError(t, err)

	resp, err := c.Get(context.Background(), srv.URL, map[string]string{
		"User-Agent": "test-agent",
		"Accept":     "application/xml",
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.Status)
	require.Equal(t, "<urlset></urlset>", string(resp.Body))
	require.False(t, resp.Truncated)
	require.Equal(t, `"abc123"`, resp.ETag)
	require.Equal(t, "Sun, 06 Sep 2026 04:00:00 GMT", resp.LastModified)
}

func TestGetCapsBodyAndFlagsTruncation(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 5000)))
	}))
	defer srv.Close()

	c, err := httpx.New(5*time.Second, httpx.WithMaxBodyBytes(1000), httpx.WithPrivateNetworksAllowed())
	require.NoError(t, err)

	resp, err := c.Get(context.Background(), srv.URL, nil)
	require.NoError(t, err)
	require.Len(t, resp.Body, 1000)
	require.True(t, resp.Truncated)
}

func TestGetDoesNotFlagTruncationAtExactLimit(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 1000)))
	}))
	defer srv.Close()

	c, err := httpx.New(5*time.Second, httpx.WithMaxBodyBytes(1000), httpx.WithPrivateNetworksAllowed())
	require.NoError(t, err)

	resp, err := c.Get(context.Background(), srv.URL, nil)
	require.NoError(t, err)
	require.Len(t, resp.Body, 1000)
	require.False(t, resp.Truncated)
}

func TestGetReturnsNonOKStatusWithoutError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("Access Denied"))
	}))
	defer srv.Close()

	c, err := httpx.New(5*time.Second, httpx.WithPrivateNetworksAllowed())
	require.NoError(t, err)

	resp, err := c.Get(context.Background(), srv.URL, nil)
	require.NoError(t, err, "an HTTP error status is a result, not a transport error")
	require.Equal(t, http.StatusForbidden, resp.Status)
	require.Equal(t, "Access Denied", string(resp.Body))
}

func TestGetHonoursContextCancellation(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer func() { close(release); srv.Close() }()

	c, err := httpx.New(5*time.Second, httpx.WithPrivateNetworksAllowed())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = c.Get(ctx, srv.URL, nil)
	require.ErrorIs(t, err, context.Canceled)
}

func TestNewRejectsBadProxyURL(t *testing.T) {
	t.Parallel()

	_, err := httpx.New(5*time.Second, httpx.WithProxy("://not a url"))
	require.Error(t, err)
}

func TestGetBoundsTheDrain(t *testing.T) {
	t.Parallel()

	const maxBody = 1000

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Write: cap + drain limit + extra, then block. Don't set
		// Content-Length so the HTTP client doesn't close the stream.
		// A bounded drain reads its limit and returns; an unbounded one
		// reads all available data, then blocks waiting for more.
		const extraData = 100000 // Extra data to make unbounded drain read more
		_, _ = w.Write(bytes.Repeat([]byte("x"), maxBody+int(4<<10)+extraData))
		w.(http.Flusher).Flush()
		// Then stop responding. A bounded drain returns anyway; an
		// unbounded one waits here for the rest of the body.
		<-release
	}))
	defer func() { close(release); srv.Close() }()

	c, err := httpx.New(5*time.Second, httpx.WithMaxBodyBytes(maxBody), httpx.WithPrivateNetworksAllowed())
	require.NoError(t, err)

	done := make(chan *httpx.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := c.Get(context.Background(), srv.URL, nil)
		if err != nil {
			errCh <- err
			return
		}
		done <- resp
	}()

	select {
	case resp := <-done:
		require.Len(t, resp.Body, maxBody)
		require.True(t, resp.Truncated)
	case err := <-errCh:
		t.Fatalf("Get failed: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("Get did not return while the handler was still holding the response open — the drain is not bounded")
	}
}
