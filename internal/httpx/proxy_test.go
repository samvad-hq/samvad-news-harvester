package httpx_test

import (
	"testing"
	"time"

	"github.com/samvad-hq/samvad-news-harvester/internal/httpx"
	"github.com/stretchr/testify/require"
)

func TestProxyCacheSharesTheDirectClient(t *testing.T) {
	t.Parallel()

	cache := httpx.NewProxyCache(time.Second, httpx.DefaultMaxBodyBytes)

	a, err := cache.For("")
	require.NoError(t, err)
	b, err := cache.For("   ")
	require.NoError(t, err)
	require.Same(t, a, b, "a blank proxy always yields the one direct client")
}

func TestProxyCacheReusesOneClientPerProxy(t *testing.T) {
	t.Parallel()

	cache := httpx.NewProxyCache(time.Second, httpx.DefaultMaxBodyBytes)

	first, err := cache.For("http://proxy.example:8080")
	require.NoError(t, err)
	second, err := cache.For("http://proxy.example:8080")
	require.NoError(t, err)
	require.Same(t, first, second)

	other, err := cache.For("http://other.example:3128")
	require.NoError(t, err)
	require.NotSame(t, first, other)

	direct, err := cache.For("")
	require.NoError(t, err)
	require.NotSame(t, first, direct)
}

func TestProxyCacheRejectsBadProxy(t *testing.T) {
	t.Parallel()

	cache := httpx.NewProxyCache(time.Second, httpx.DefaultMaxBodyBytes)
	_, err := cache.For("not-a-url")
	require.Error(t, err)
}

// TestWithProxyRejectsABadProxyURLWithoutLeakingItsPassword pins finding
// #3: a proxy URL is a normal place to carry basic-auth credentials, and
// the old error message echoed the raw URL with %q on a validation
// failure.
func TestWithProxyRejectsABadProxyURLWithoutLeakingItsPassword(t *testing.T) {
	t.Parallel()

	_, err := httpx.New(time.Second, httpx.WithProxy("http://user:hunter2@"))
	require.Error(t, err)
	require.NotContains(t, err.Error(), "hunter2")
}

func TestProxyCacheIsSafeForConcurrentUse(t *testing.T) {
	t.Parallel()

	cache := httpx.NewProxyCache(time.Second, httpx.DefaultMaxBodyBytes)
	done := make(chan struct{})

	for range 16 {
		go func() {
			defer func() { done <- struct{}{} }()
			_, err := cache.For("http://proxy.example:8080")
			require.NoError(t, err)
		}()
	}
	for range 16 {
		<-done
	}
}
