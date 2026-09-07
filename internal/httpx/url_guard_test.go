package httpx_test

// These tests pin the URL-level guard added alongside the existing
// dial-level hook: guardedDialer's Control func never sees the real
// destination once a proxy is configured (every dial it observes targets
// the proxy itself), so a proxied client previously had no SSRF
// protection at all. None of these requests need to actually reach a
// proxy or a blocked address — the guard runs before the request is
// issued, so a non-routable placeholder proxy URL is enough.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/samvad-hq/samvad-news-harvester/internal/httpx"
	"github.com/stretchr/testify/require"
)

// placeholderProxy is a syntactically valid proxy URL that is never
// actually dialed by the three refusal tests below: the guard runs
// before the request is issued, so it never reaches this address.
const placeholderProxy = "http://proxy.example:8080"

func TestGetWithProxyRefusesTheCloudMetadataAddress(t *testing.T) {
	t.Parallel()

	c, err := httpx.New(5*time.Second, httpx.WithProxy(placeholderProxy))
	require.NoError(t, err)

	_, err = c.Get(context.Background(), "http://169.254.169.254/latest/meta-data/", nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "private network")
}

func TestGetWithProxyRefusesAPrivateIPLiteral(t *testing.T) {
	t.Parallel()

	c, err := httpx.New(5*time.Second, httpx.WithProxy(placeholderProxy))
	require.NoError(t, err)

	_, err = c.Get(context.Background(), "http://10.1.2.3/", nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "private network")
}

func TestGetWithoutProxyStillRefusesBothAddressesAtTheURLLevel(t *testing.T) {
	t.Parallel()

	c, err := httpx.New(5 * time.Second)
	require.NoError(t, err)

	_, err = c.Get(context.Background(), "http://169.254.169.254/", nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "private network")

	_, err = c.Get(context.Background(), "http://192.168.1.1/", nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "private network")
}

// TestGetWithProxyAndPrivateNetworksAllowedSkipsTheURLLevelGuard confirms
// WithPrivateNetworksAllowed also disables the new URL-level check, not
// just the dial-level one. The "proxy" here is a real local httptest
// server standing in for one: net/http forwards a plain-HTTP proxied
// request to it as an ordinary request carrying the absolute target URL,
// so a 200 from this handler proves the request reached all the way
// through — including past the point where the guard would have
// refused a blocked target — without any real outbound network access.
func TestGetWithProxyAndPrivateNetworksAllowedSkipsTheURLLevelGuard(t *testing.T) {
	t.Parallel()

	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer proxy.Close()

	c, err := httpx.New(5*time.Second, httpx.WithProxy(proxy.URL), httpx.WithPrivateNetworksAllowed())
	require.NoError(t, err)

	resp, err := c.Get(context.Background(), "http://169.254.169.254/", nil)
	require.NoError(t, err, "opting out must skip the URL-level guard, not just the dial-level one")
	require.Equal(t, http.StatusOK, resp.Status)
}
