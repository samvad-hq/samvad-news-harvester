package httpx_test

import (
	"context"
	"testing"
	"time"

	"github.com/samvad-hq/samvad-news-harvester/internal/httpx"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

func TestHostLimiterThrottlesPerHost(t *testing.T) {
	t.Parallel()

	// 20 requests/sec, burst 1: the second call to a host waits ~50ms.
	lim := httpx.NewHostLimiter(rate.Limit(20), 1)
	ctx := context.Background()

	start := time.Now()
	require.NoError(t, lim.Wait(ctx, "https://a.example/one"))
	require.NoError(t, lim.Wait(ctx, "https://a.example/two"))
	require.GreaterOrEqual(t, time.Since(start), 30*time.Millisecond)
}

func TestHostLimiterKeysByHostNotURL(t *testing.T) {
	t.Parallel()

	lim := httpx.NewHostLimiter(rate.Limit(20), 1)
	ctx := context.Background()

	require.NoError(t, lim.Wait(ctx, "https://a.example/one"))

	// A different host has its own budget, so this returns immediately.
	start := time.Now()
	require.NoError(t, lim.Wait(ctx, "https://b.example/one"))
	require.Less(t, time.Since(start), 20*time.Millisecond)
}

func TestHostLimiterHonoursCancellation(t *testing.T) {
	t.Parallel()

	lim := httpx.NewHostLimiter(rate.Limit(1), 1)
	ctx, cancel := context.WithCancel(context.Background())

	require.NoError(t, lim.Wait(ctx, "https://a.example/one"))
	cancel()
	require.Error(t, lim.Wait(ctx, "https://a.example/two"))
}

func TestHostLimiterAllowsUnparseableURLThrough(t *testing.T) {
	t.Parallel()

	lim := httpx.NewHostLimiter(rate.Limit(20), 1)
	require.NoError(t, lim.Wait(context.Background(), "::not a url"))
}
