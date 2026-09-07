// Package httpx wraps net/http with the three things every fetch in this
// service needs: a hard cap on how much of a response body is read, the
// cache validators that make conditional requests possible, and an
// optional per-client proxy that leaves the rest of the process alone.
package httpx

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// stripUserinfo returns u without any embedded credentials, for safe use
// in an error message. A proxy URL is a normal place to carry basic-auth
// credentials, and the caller already has a parsed *url.URL in hand at
// every point this is used, so there is never a need to fall back to
// echoing the raw string that might contain them.
func stripUserinfo(u *url.URL) string {
	stripped := *u
	stripped.User = nil
	return stripped.String()
}

// DefaultMaxBodyBytes bounds a response body. The largest configured
// sitemap (jagran) is about 2.5 MB, so 8 MiB leaves substantial headroom
// while still refusing to buffer an unbounded response.
const DefaultMaxBodyBytes int64 = 8 << 20

// drainLimit bounds how much of an over-cap response is drained to reuse
// the connection. Draining the entire body defeats the purpose of a body
// cap, so we give up reuse instead. 4 KiB is conventional.
const drainLimit int64 = 4 << 10

// Response is the part of an HTTP response this service uses. Body is
// already read and capped, so the caller never handles an open stream.
type Response struct {
	// Status is the HTTP status code. A non-2xx status is reported here,
	// not as an error — only transport failures produce an error.
	Status int
	// Body holds at most MaxBodyBytes of the response.
	Body []byte
	// Truncated reports that the response was longer than the cap.
	Truncated bool
	// ETag and LastModified carry the cache validators, so a caller can
	// make the next request conditional.
	ETag         string
	LastModified string
}

// Client performs capped GET requests.
type Client struct {
	hc      *http.Client
	maxBody int64
	// guarded is true unless the caller opted out via
	// WithPrivateNetworksAllowed. It gates the URL-level check Get runs
	// before every request, in addition to the dial-level hook that is
	// always wired into the transport's dialer for a non-opted-out
	// client — see guardURL's doc comment for why both exist.
	guarded bool
}

// Option configures a Client.
type Option func(*clientConfig) error

type clientConfig struct {
	proxy        *url.URL
	maxBody      int64
	allowPrivate bool
}

// WithMaxBodyBytes caps how much of a response body is read. A value of
// zero or less leaves the default in place. Absurdly large values are clamped
// to prevent overflow in the internal cap computation.
func WithMaxBodyBytes(n int64) Option {
	return func(c *clientConfig) error {
		if n > 0 {
			// Clamp to math.MaxInt64 - 1 to prevent overflow when computing maxBody+1.
			if n > math.MaxInt64-1 {
				n = math.MaxInt64 - 1
			}
			c.maxBody = n
		}
		return nil
	}
}

// WithProxy routes this client's requests through rawURL. The proxy is
// scoped to the client rather than the process, so it never affects other
// traffic. A blank rawURL is a no-op.
func WithProxy(rawURL string) Option {
	return func(c *clientConfig) error {
		rawURL = strings.TrimSpace(rawURL)
		if rawURL == "" {
			return nil
		}
		u, err := url.Parse(rawURL)
		if err != nil {
			return fmt.Errorf("httpx: parse proxy url: %w", err)
		}
		if u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("httpx: proxy url %q must include a scheme and host", stripUserinfo(u))
		}
		c.proxy = u
		return nil
	}
}

// WithPrivateNetworksAllowed disables the default guard against dialing a
// loopback, link-local, unique-local, RFC1918-private, or unspecified
// address. It is off by default because sitemap and article URLs come
// from third-party XML a publisher controls: leaving the guard on stops a
// compromised or hostile publisher from using this client to probe the
// operator's own network via a crafted <loc> or an HTTP redirect. Tests
// that talk to an httptest server on loopback must opt in explicitly.
func WithPrivateNetworksAllowed() Option {
	return func(c *clientConfig) error {
		c.allowPrivate = true
		return nil
	}
}

// New builds a client with the given per-request timeout.
func New(timeout time.Duration, opts ...Option) (*Client, error) {
	cfg := clientConfig{maxBody: DefaultMaxBodyBytes}
	for _, opt := range opts {
		if err := opt(&cfg); err != nil {
			return nil, err
		}
	}

	// http.ProxyURL(nil) returns a func yielding (nil, nil), which net/http
	// treats the same as a nil Proxy field. Either way, HTTP_PROXY and
	// HTTPS_PROXY environment variables are bypassed for this client.
	dialer := guardedDialer(cfg)
	transport := &http.Transport{
		Proxy:               http.ProxyURL(cfg.proxy),
		DialContext:         dialer.DialContext,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		ForceAttemptHTTP2:   true,
	}

	return &Client{
		hc:      &http.Client{Timeout: timeout, Transport: transport},
		maxBody: cfg.maxBody,
		guarded: !cfg.allowPrivate,
	}, nil
}

// Get performs a GET and reads at most the configured body cap. A non-2xx
// status is returned in the Response; only a transport or context failure
// produces an error.
func (c *Client) Get(ctx context.Context, rawURL string, headers map[string]string) (*Response, error) {
	if c.guarded {
		if err := guardURL(ctx, rawURL); err != nil {
			return nil, err
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("httpx: build request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("httpx: get %s: %w", rawURL, err)
	}
	defer func() {
		// Drain a little so the connection can be reused, but never the
		// whole body: an over-cap response is exactly the case where
		// reading to the end is the thing we are trying to avoid. Give up
		// reuse instead, which costs one handshake.
		_, _ = io.CopyN(io.Discard, resp.Body, drainLimit)
		_ = resp.Body.Close()
	}()

	// Read one byte past the cap so a body that exactly fills it is not
	// misreported as truncated.
	body, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("httpx: read body from %s: %w", rawURL, err)
	}

	out := &Response{
		Status:       resp.StatusCode,
		ETag:         resp.Header.Get("ETag"),
		LastModified: resp.Header.Get("Last-Modified"),
	}
	if int64(len(body)) > c.maxBody {
		out.Body = body[:c.maxBody]
		out.Truncated = true
	} else {
		out.Body = body
	}
	return out, nil
}
