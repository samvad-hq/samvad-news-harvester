# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.2.0] - Unreleased

A full rewrite of the service internals, plus the throughput work that
followed it. **This release is breaking**: every environment variable,
both configuration file schemas, two event field names, and the article
ID derivation all changed. See
[docs/configuration.md](docs/configuration.md#migration-from-v1) for the
complete v1-to-v2 mapping.

### Added

- **`docs/architecture.md`** — the five pipeline stages with measured
  cost figures, the concurrency model, a failure-handling table per
  stage, and an inventory of every interface in the codebase with the
  rule for why each one exists.
- **`docs/configuration.md`** — the full environment variable table, both
  YAML schemas, the URL canonicalisation rules, and the v1-to-v2
  migration table.
- **A Redis dedupe backend**, selected with `DEDUPE_BACKEND=redis` and
  `DEDUPE_REDIS_URL`. It exists for hosts with an ephemeral filesystem —
  Cloud Run, Railway and similar lose the bbolt file on every redeploy, and
  the next crawl then republishes every article as if it had never been
  seen. Keys are `dedupe:article:<article id>` and carry `DEDUPE_TTL` as
  their own expiry, so this backend has no sweeper and ignores
  `DEDUPE_CLEANUP_INTERVAL`. A batch costs one `MGET` and one pipelined
  write per source, matching bolt's one-transaction-per-source shape. It
  does not make the harvester horizontally scalable; see
  [docs/configuration.md](docs/configuration.md#choosing-a-dedupe-backend).
- **`DELIVERY_CONCURRENCY`** (default `8`) bounds how many of one
  source's articles are delivered to the sinks at once. Set it to `1` for
  a sink that is aggressively rate-limited.
- **The per-source `proxy` field** in `sources.yaml`, routing one
  source's requests through a proxy without affecting any other source or
  the rest of the process. Three of the 26 configured sources answer 403
  to a direct request and need it; see the README's "Blocked publishers"
  section.
- **`scripts/capture-fixtures.sh`**, which records live sitemap XML as
  test fixtures, so a publisher changing their document shape breaks CI
  rather than production.
- **`DEDUPE_BACKEND=none`**, an explicit no-op dedupe store, replacing the
  v1 ambiguity of `STORAGE_TYPE=none`.

### Changed

- **Every environment variable was renamed** (`PROVIDERS_FILE` →
  `SOURCES_FILE`, `STORAGE_TYPE` → `DEDUPE_BACKEND`, and eight more), and
  several switched from raw seconds to Go duration strings
  (`CRAWL_INTERVAL=900` → `CRAWL_INTERVAL=15m`).
- **`configs/providers.yaml` is now `configs/sources.yaml`**: `providers:`
  → `sources:`, `source_url:` → `url:`, the untyped `config:` header map
  is now a typed `headers:` struct, and the only source `type` is now
  `news_sitemap` (was `google_news_sitemap`).
- **`configs/publishers.yaml` is now `configs/sinks.yaml`**: `publishers:`
  → `sinks:`, and the nested `type: queue` / `queue.provider: aws-sqs`
  shape is now a flat `type: aws-sqs` with its own `sqs:` block.
- **The event JSON renamed `provider_id` to `source_id`** and
  `provider_name` to `source_name`. A v1 consumer must be updated for this
  before it reads v2 events, or it will silently read `""` for both.
- **Article IDs changed** from the SHA-1 of the raw URL to the SHA-256 of
  the canonical URL. **The dedupe database does not migrate** — delete the
  old file before running v2; see
  [the migration section](docs/configuration.md#migration-from-v1).
- **Delivery semantics changed** from a claimed (but never actually
  delivered) exactly-once to an honest at-least-once. An article is marked
  delivered only once every enabled sink has accepted it, so a partial
  failure redelivers the event to every sink — including the ones that
  already got it — on the next crawl. Consumers must key on `article.id`;
  see [the README](README.md#delivery-is-at-least-once).
- **Delivery is concurrent within a source.** The pipeline previously sent
  one article at a time and waited for every sink to answer before
  starting the next, so a source with 2500 fresh articles paid the slowest
  sink's latency 2500 times in series. Articles are now delivered through
  an `errgroup.Group` bounded by `DELIVERY_CONCURRENCY`. This is also what
  makes Pub/Sub's client-side bundler useful: it coalesces messages that
  are in flight together, and before this at most one `Send` per topic was
  in flight per source.
- **A sink no longer receives one source's articles in sitemap order.**
  Completion order under concurrent delivery is arbitrary. Consumers key
  on `article.id` (as they already must, delivery being at-least-once) and
  order by `article.published_at`; both are on every event.
- **Logging moved from a custom `zap`-backed `Logger` interface to the
  standard library's `log/slog`**, emitting structured JSON to stdout.
- **Rate limiting moved** from a fixed `request_delay_ms` per source,
  shared across a ticker-throttled worker pool that only took turns, to a
  `PER_HOST_RPS` limiter keyed on hostname and shared process-wide across
  every request to that host.
- **Adding a source type no longer means implementing an exported
  interface.** `pkg/providers.Fetcher` and its registry are gone: a source
  type is now a concrete struct with a `Fetch` method plus one line in
  `cmd/harvester/main.go`'s fetcher map. See
  [the README](README.md#adding-a-source-type).

### Fixed

Four correctness defects:

- **Shutdown deadlock.** The scrape worker pool's producer checked
  `ctx.Err()` and then blocked on an unbuffered channel send; if
  cancellation landed between the check and the send, every worker had
  already exited and the producer blocked forever. There is no hand-rolled
  channel pool any more — `errgroup.WithContext` inside the enrich stage
  removes the race structurally.
- **Partial-delivery data loss.** The fan-out reported a success *count*,
  and the caller marked an article delivered whenever the count was above
  zero. If one sink accepted an event and another rejected it, the article
  was recorded as delivered and the rejecting sink never saw it again —
  not on the next crawl, not ever. The fan-out now reports which sinks
  accepted and which failed, and the article is marked only when the
  failure set is empty.
- **URL canonicalisation.** Article IDs were the SHA-1 of the raw sitemap
  URL, so a tracking parameter, a trailing slash, or a host in different
  case produced a different ID for what was the same article — and dedupe
  missed the exact case it exists to catch. IDs are now the SHA-256 of a
  canonical form: lowercased scheme and host, default port and fragment
  removed, tracking parameters stripped, remaining parameters sorted, and
  a trailing slash trimmed.
- **Exit code on shutdown.** A misconfigured deployment with no sources
  returned `ctx.Err()` from the run path, which surfaced as exit code 1 —
  indistinguishable from a crash to an orchestrator, and a plain SIGTERM
  produced the same restart-triggering exit. A cancelled context is now a
  clean shutdown that exits 0.

Three throughput defects:

- **Dedupe write-lock contention.** Checking whether an article had been
  seen used a bbolt *write* transaction, so it could lazily delete expired
  keys on the way past — and bbolt allows only one writer at a time, so
  every dedupe lookup serialised the entire crawl on that lock. Lookups
  now use a read transaction; an expired key simply reads as unseen and a
  periodic sweep reclaims it.
- **Per-article fsync.** Marking articles delivered opened one bbolt
  transaction per article. For `thedailyjagran`, which publishes about
  2500 URLs per sitemap, that was 2500 transactions and 2500 fsyncs
  contending for the same lock the reads above were already waiting on. A
  source's whole batch is now marked in one transaction.
- **A rate limiter that starved rather than limited.** Ten scrape workers
  shared a single `time.Ticker`, so the pool's effective throughput was
  one request total per tick regardless of how many workers were idle —
  the workers only took turns waiting on the same clock, with nothing
  bounding how many sources' scrapes ran against one host at once. Rate
  limiting is now a `golang.org/x/time/rate.Limiter` per hostname, so
  concurrency and pacing are governed independently.

And four defects found reviewing the rewrite itself:

- **One oversized sitemap no longer delays every other source's next
  crawl as much as it could.** A single `<urlset>` document is capped at
  5000 `<url>` entries; an oversized document is truncated and logs a
  warning naming the source, the URL, and both counts. Google's news
  sitemap specification caps a document at 1000 URLs, so this is headroom
  past any compliant publisher.
- **A sustained sink outage no longer produces an unbounded log line.**
  `Fanout.Send` already logs each failed sink at the point of failure;
  the delivery stage joining every article's own error on top of that
  repeated the same detail once per article. It now reports "N of M
  articles failed delivery".
- **`cmd/harvester -once` exits non-zero when every source failed.**
  Previously any failure was swallowed and the process always exited 0,
  so a cron wrapper could not detect a total outage. The exit is
  conditional on *every* source failing, because three of the 26
  configured sources answer 403 by design and exiting non-zero on any
  single failure would make the exit code permanently red; see the
  README's exit-code section.
- **A bad `og:image` no longer overwrites a good sitemap image.** The
  enrich stage returned the raw, unparseable string on a URL parse
  failure and wrote that over a valid sitemap `ImageURL`. It now returns
  nothing on failure, and only overwrites when it gets something back.

### Security

- **SSRF via publisher-supplied XML.** Sitemap index `<loc>` children and
  article URLs come from third-party XML and were fetched with no
  restriction on the destination. `internal/httpx.Client` now refuses by
  default to dial a loopback, link-local, unique-local, RFC1918-private,
  carrier-grade-NAT (`100.64.0.0/10`), multicast, limited-broadcast
  (`255.255.255.255`) or unspecified address, checked on every resolved
  address the transport actually dials — including one reached only via a
  redirect. A second, URL-level check runs before the request is issued,
  because the dial-level hook is blind for a client with a proxy
  configured: every dial it sees targets the proxy's own address rather
  than the request's real destination. That second layer is advisory for a
  proxied client, since the proxy resolves independently and can disagree;
  [docs/architecture.md](docs/architecture.md) states the limit precisely.
  `httpx.WithPrivateNetworksAllowed()` opts a client out and is used only
  by tests talking to an `httptest` server on loopback. A complementary
  check in `internal/source` skips a sitemap index child whose host does
  not loosely match its index document's host, logging both hosts.
- **Webhook secrets no longer reach the log.** For Slack, Discord and
  Microsoft Teams incoming webhooks the URL path is the bearer credential.
  The HTTP sink previously wrapped a transport failure with the raw URL,
  and net/http's own `*url.Error` embeds it a second time regardless — so
  a transient timeout wrote the credential to stdout twice. Sink
  validation leaked it a third way, quoting the raw URL when the scheme or
  host was invalid. Every one of those paths now uses a redacted form
  (scheme and host only); the real URL is used solely to build the
  request. A non-2xx response body is still quoted, bounded to 512 bytes,
  since that comes from the far end rather than from the URL.
- **Credentials no longer appear in configuration errors.** A malformed
  proxy URL's basic-auth userinfo is stripped from the resulting
  validation error in both `internal/httpx` and `internal/source`. SQS and
  SNS access keys and secrets, and the Pub/Sub credentials file, are only
  ever reported as missing by name, never with their value. The AWS and
  GCP identifiers (queue URL, topic ARN, project ID, topic) are not
  secrets and are reported as-is.

### Removed

- `pkg/providers`, `pkg/publishers`, `internal/crawler`, `internal/storage`,
  `internal/logger`, `internal/domain`, `internal/util`, `internal/scheduler`.
- Dependencies: `viper` and its transitive pull of `afero`, `mapstructure`,
  `pflag`, `cast`, `toml`, `ini` and `fsnotify`; `zap`; `resty`.
- The `type: queue` publisher shape and its nested `queue.provider` /
  `queue.aws` fields, replaced by a flat sink `type` per cloud provider.
- `request_delay_ms` and `response_format` from the source schema — the
  former is superseded by `PER_HOST_RPS`, the latter was never read.

## [0.1.0] - 2026-06-16

The original harvester, as it ran in production for nine months against
26 Indian news sitemaps. Recorded here for reference; it predates this
changelog and was never tagged at the time.

Google News sitemap crawling with a per-provider proxy, Open Graph
metadata scraping, bbolt-backed deduplication, and fan-out to HTTP, AWS
SQS, AWS SNS and GCP Pub/Sub publishers, configured through `viper`,
`zap` and `resty`.
