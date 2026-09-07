// Package dedupe remembers which articles have already been delivered, so
// a source that lists the same story on every crawl only produces one
// event.
//
// There are three stores: Bolt writes to a local file, Redis to an
// external instance for hosts whose filesystem does not survive a
// redeploy, and Noop remembers nothing. All of them work in batches. A
// single crawl of one source can carry thousands of article IDs —
// thedailyjagran publishes about 2500 — and asking one question per
// article is what made the old store the throughput ceiling of the whole
// service.
package dedupe

import "context"

// Noop is the store used when deduplication is disabled. Every article is
// unseen, so every crawl republishes everything.
type Noop struct{}

// Unseen returns ids unchanged.
func (Noop) Unseen(_ context.Context, ids []string) ([]string, error) { return ids, nil }

// Mark does nothing.
func (Noop) Mark(context.Context, []string) error { return nil }

// Close does nothing.
func (Noop) Close() error { return nil }
