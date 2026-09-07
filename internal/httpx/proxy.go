package httpx

import (
	"strings"
	"sync"
	"time"
)

// ProxyCache hands out one Client per proxy URL. A blank URL yields a
// shared direct client. Clients are built on first use and reused, so a
// crawl does not construct a transport per request. Safe for concurrent use.
type ProxyCache struct {
	timeout time.Duration
	maxBody int64
	opts    []Option
	direct  *Client

	mu      sync.Mutex
	byProxy map[string]*Client
}

// NewProxyCache returns a cache whose clients share the given timeout and
// body cap, plus any further options — for example
// WithPrivateNetworksAllowed, which a test talking to an httptest server
// on loopback needs to pass through to every client the cache builds, not
// just the first one. It panics only if the direct client cannot be
// built, which cannot happen for an empty proxy.
func NewProxyCache(timeout time.Duration, maxBody int64, opts ...Option) *ProxyCache {
	direct, err := New(timeout, append([]Option{WithMaxBodyBytes(maxBody)}, opts...)...)
	if err != nil {
		// New only fails on a malformed proxy, and there is none here.
		panic("httpx: building the direct client cannot fail: " + err.Error())
	}
	return &ProxyCache{
		timeout: timeout,
		maxBody: maxBody,
		opts:    opts,
		direct:  direct,
		byProxy: make(map[string]*Client),
	}
}

// For returns the client for proxyURL: the shared direct client when the
// URL is blank, otherwise a per-URL client created on first use.
func (c *ProxyCache) For(proxyURL string) (*Client, error) {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return c.direct, nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if cl, ok := c.byProxy[proxyURL]; ok {
		return cl, nil
	}
	opts := append([]Option{WithMaxBodyBytes(c.maxBody), WithProxy(proxyURL)}, c.opts...)
	cl, err := New(c.timeout, opts...)
	if err != nil {
		return nil, err
	}
	c.byProxy[proxyURL] = cl
	return cl, nil
}
