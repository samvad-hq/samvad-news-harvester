package httpclient

import (
	"strings"
	"sync"
	"time"
)

// ProxyCache hands out HTTP clients keyed by proxy URL. A blank URL yields a
// shared direct (no-proxy) client; each distinct proxy URL gets its own client,
// built once on first use and reused. Safe for concurrent use.
type ProxyCache struct {
	timeout time.Duration
	direct  Client

	mu      sync.Mutex
	byProxy map[string]Client
}

// NewProxyCache returns a cache that builds clients with the given timeout.
// A nil direct client defaults to a plain (no-proxy) client.
func NewProxyCache(timeout time.Duration, direct Client) *ProxyCache {
	if direct == nil {
		direct = NewRestyClient(timeout)
	}
	return &ProxyCache{
		timeout: timeout,
		direct:  direct,
		byProxy: make(map[string]Client),
	}
}

// For returns the client for proxyURL: the shared direct client when blank,
// otherwise a per-URL client created on first use and cached.
func (c *ProxyCache) For(proxyURL string) Client {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return c.direct
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if cl, ok := c.byProxy[proxyURL]; ok {
		return cl
	}
	cl := NewRestyClientWithProxy(c.timeout, proxyURL)
	c.byProxy[proxyURL] = cl
	return cl
}
