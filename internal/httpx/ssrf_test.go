package httpx

// This file is in-package (not httpx_test) because
// TestGetRefusesARedirectToAPrivateAddress needs to hand the guarded
// dialer a fabricated first hop that never actually dials anywhere — see
// its doc comment for why a real two-httptest-server redirect can't prove
// what this test needs to prove.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGetRefusesAPrivateAddressByDefault(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := New(5 * time.Second)
	require.NoError(t, err)

	_, err = c.Get(context.Background(), srv.URL, nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "private network")
}

func TestGetAllowsAPrivateAddressWhenOptedIn(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c, err := New(5*time.Second, WithPrivateNetworksAllowed())
	require.NoError(t, err)

	resp, err := c.Get(context.Background(), srv.URL, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.Status)
}

// fakeFirstHopTransport answers exactly one URL itself, with a canned
// redirect, and never dials anything to do it. It exists so
// TestGetRefusesARedirectToAPrivateAddress can prove the guard runs again
// on the redirect target reached through the *same* client, without that
// proof being confounded by the fact that in this test environment there
// is no way to bind a listener to an address the guard would treat as
// public — every address an httptest.Server can use is itself loopback,
// so a genuine two-real-server redirect chain would already be refused at
// the first hop, never exercising the second one at all. Faking the first
// hop's response removes that hop from the guarded dial path entirely, so
// the only real dial in the test is the one to the redirect target.
type fakeFirstHopTransport struct {
	outerURL string
	location string
	next     http.RoundTripper
}

func (f *fakeFirstHopTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.String() == f.outerURL {
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{f.location}},
			Body:       http.NoBody,
			Request:    req,
		}, nil
	}
	return f.next.RoundTrip(req)
}

func TestGetRefusesARedirectToAPrivateAddress(t *testing.T) {
	t.Parallel()

	inner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer inner.Close()

	cfg := clientConfig{maxBody: DefaultMaxBodyBytes}
	dialer := guardedDialer(cfg)
	guardedTransport := &http.Transport{DialContext: dialer.DialContext}

	const outerURL = "http://outer.invalid/redirect"
	c := &Client{
		hc: &http.Client{
			Transport: &fakeFirstHopTransport{outerURL: outerURL, location: inner.URL, next: guardedTransport},
		},
		maxBody: cfg.maxBody,
	}

	_, err := c.Get(context.Background(), outerURL, nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "private network")
}
