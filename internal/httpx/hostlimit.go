package httpx

import (
	"context"
	"net/url"
	"sync"

	"golang.org/x/time/rate"
)

// HostLimiter paces outbound requests per host.
//
// Keying on host rather than on source matters for the article scrape, not
// the sitemap fetch: the configured sources span one hostname each, so
// fetches never contend, but a single crawl of one source issues hundreds
// of article requests that all resolve to that source's host. A per-source
// limit would also let two sources belonging to one publisher run
// unthrottled against it.
//
// Safe for concurrent use.
type HostLimiter struct {
	limit rate.Limit
	burst int

	mu     sync.Mutex
	byHost map[string]*rate.Limiter
}

// NewHostLimiter returns a limiter allowing perHost requests per second to
// each host, with the given burst.
func NewHostLimiter(perHost rate.Limit, burst int) *HostLimiter {
	if burst < 1 {
		burst = 1
	}
	return &HostLimiter{
		limit:  perHost,
		burst:  burst,
		byHost: make(map[string]*rate.Limiter),
	}
}

// Wait blocks until the host behind rawURL has budget, or until ctx is
// done. A URL that cannot be parsed is let through rather than rejected:
// the caller's own request will fail on it and report a better error.
func (l *HostLimiter) Wait(ctx context.Context, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return nil //nolint:nilerr
	}
	return l.forHost(u.Hostname()).Wait(ctx)
}

// forHost returns the limiter for host, creating it on first use.
func (l *HostLimiter) forHost(host string) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()

	if lim, ok := l.byHost[host]; ok {
		return lim
	}
	lim := rate.NewLimiter(l.limit, l.burst)
	l.byHost[host] = lim
	return lim
}
