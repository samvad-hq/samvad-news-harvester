# Architecture

This document covers the crawl pipeline in `internal/harvest`, the
concurrency model that bounds it, what happens when each stage fails, and
the seven interfaces in the codebase and why each exists.

## The five stages

One pass over the source list runs each configured source through five
stages, in order. Within a source the stages are sequential — each depends
on the output of the last — but sources themselves run concurrently, and
the two stages that work per article (enrich and deliver) run their
articles concurrently within the stage (see
[Concurrency](#concurrency) below).

### 1. Fetch

**Package:** `internal/source` (`Sitemap.Fetch`)

Performs one HTTP GET against the source's `url`. If the document is a
`<sitemapindex>` rather than a plain `<urlset>`, its children are fetched
in turn (bounded to a depth of 4 and 200 children, to guard against a
pathological or malicious index). The root element is inspected before
parsing, so an HTML challenge page or an RSS feed served where a sitemap
was expected produces an error rather than silently reporting zero
articles forever.

A single `<urlset>` document is capped at 5000 `<url>` entries
(`source.maxURLsPerSitemap`); an oversized document is truncated to the
cap rather than fully processed. This exists because `Harvester.Run` calls
`RunOnce` synchronously on each tick: one oversized sitemap that takes
long enough to fetch and enrich delays the next crawl of every other
source, not just its own. Google's news sitemap specification caps a
single document at 1000 URLs, so 5000 is headroom past any compliant
publisher, not a limit a legitimate one is expected to hit — see
[the failure-handling table](#failure-handling) for what happens to the
dropped entries (nothing retries them; they reappear only if the
publisher's next document lists them again within the cap).

A sitemap index child whose host does not loosely match the index
document's own host is logged and skipped rather than fetched — both a
Fetch and an Enrich URL come from third-party XML a publisher controls,
so this is a cheap, source-local defense alongside the transport-level one
below. "Loosely" means comparing a registrable-looking suffix
(`source.sameRegistrableHost`), so a publisher sharding its sitemap onto
its own CDN subdomain still works; an index child on a genuinely unrelated
domain does not.

**SSRF guard.** Both this stage's sitemap fetches and the enrich stage's
article fetches go through `httpx.Client`, which by default refuses to
dial a loopback, link-local, unique-local, RFC1918-private,
carrier-grade-NAT (`100.64.0.0/10`), multicast, limited-broadcast
(`255.255.255.255`), or unspecified address. This exists because both
kinds of URL originate in XML a publisher supplies: without it, a
compromised or hostile publisher could point a `<loc>` or an
`og:image`-adjacent redirect at `169.254.169.254`, at loopback, or at a
service on the operator's own network, and this process would fetch it
on their behalf.

The guard has two layers. A dial-level hook (`net.Dialer.Control`) checks
every resolved address the transport actually dials, including one
reached only after a redirect — net/http dials again through the same
hook for the new address, so a redirect gets no extra pass. A second,
URL-level check runs in `Client.Get` before the request is issued at
all, checking the target URL's host (resolving it if it is a hostname)
against the same blocklist. The URL-level check exists because the
dial-level hook is blind for a client with an explicit proxy configured
(`source.Config.Proxy`): every dial `Control` sees in that case targets
the proxy's own address, never the request's real destination — an
HTTPS request reaches it by tunnelling a CONNECT through that same
connection, which the hook never sees — so before this second layer
existed, a proxied source had no SSRF protection at all. The proxy
address itself is exempt from both layers, because it is operator
configuration, not something publisher XML can influence, and must keep
working even when it points at a private network.

The two layers are not equally strong. For a direct (non-proxied) client
the URL-level check duplicates the dial-level one — same resolution,
same result — so it only fails a step sooner. For a proxied client it is
advisory only: resolution happens twice, once in this check and once
inside the proxy, and nothing guarantees they agree. A hostname can
legitimately resolve differently between the two lookups, and a hostile
publisher racing a DNS change between them (a rebind) can pass this
check with a public address while the proxy still connects the request
to a private one. That window is real and this guard cannot close it for
a proxied client; it still blocks the common case of a URL that names a
private address directly or a hostname that only ever resolves to one.

Tests that talk to an `httptest` server on loopback opt out explicitly
with `httpx.WithPrivateNetworksAllowed()`, which disables both layers;
production code never does.

**Cost, measured on 2026-09-06 against the then-configured 10 sources:** 8
of 10 answered 200 with a browser-like `User-Agent` header; the other two
answered 403 and needed the `proxy` field described in
[configuration.md](configuration.md). `jagran` (now `thedailyjagran`), the
largest sitemap, ships about 2500 URLs at roughly 2.5 MB. Across the eight
working sources, one crawl transferred about 4.6 MB total — which, at the
default 15-minute `CRAWL_INTERVAL`, is roughly 440 MB a day. The service
is now configured with 26 sources, so the current steady-state figure is
proportionally higher; these are the baseline numbers the body cap and
timeout defaults were chosen against.

### 2. Identify

**Package:** `internal/news` (`CanonicalURL`, `ID`)

Every sitemap `<loc>` is canonicalised and hashed to produce the article's
permanent ID before anything else touches it. This is pure CPU work with no
I/O, and it is what makes deduplication, delivery and consumer-side
idempotency all agree on what "the same article" means. See
[configuration.md](configuration.md#url-canonicalisation) for the exact
canonicalisation rules.

### 3. Dedupe

**Package:** `internal/dedupe` (`Bolt.Unseen` / `Noop.Unseen`)

The whole batch of IDs from one source is checked in a single bbolt read
transaction (`db.View`), not one transaction per article. bbolt allows only
one writer at a time but any number of concurrent readers, so a read
transaction here does not block the sitemap fetch or the scrape stage of
any other source running concurrently.

### 4. Enrich

**Package:** `internal/enrich` (`Scraper.Enrich`)

A sitemap `<url>` record carries a title and a publish date but no
description or image, so each fresh article's page is fetched and its Open
Graph (falling back to Twitter Card, then plain `<meta name="description">`
and `<title>`) tags are read. This is the expensive stage: it is one HTTP
request per article, bounded by `ARTICLE_CONCURRENCY` in-flight requests
per source and paced by the shared per-host limiter (see below).
Enrichment cannot fail the crawl — a page that will not load, times out, or
answers non-200 simply leaves the article with the title the sitemap
already supplied.

### 5. Deliver

**Package:** `internal/sink` (`Fanout.Send`) and `internal/harvest`
(`runSource`/`deliver`)

Each fresh, enriched article is wrapped in an `Event` and sent to every
enabled sink concurrently, and the articles themselves are delivered
concurrently too, up to `DELIVERY_CONCURRENCY` of one source's articles at
a time. `sink.Sink` documents that `Send` must be safe for concurrent use,
which every implementation is. An article is marked delivered — and
therefore excluded from future crawls — only when every sink accepted it;
see
[the README](../README.md#delivery-is-at-least-once) for the consumer
contract this creates. IDs that were fully delivered are marked in one
bbolt write transaction per source (`Deduper.Mark`), for the same reason
reads are batched: one fsync per source rather than one per article.

**A sink no longer receives one source's articles in sitemap order.**
Delivering them concurrently means completion order is arbitrary, so a
webhook or queue consumer sees a source's batch interleaved rather than
in the order the sitemap listed it. Nothing downstream may depend on that
order: consumers key on `article.id` and order by `article.published_at`,
both of which are carried on every event. The IDs handed to `Deduper.Mark`
*are* still in article order — each delivery writes its outcome to its own
slot rather than appending as it finishes — but that is an internal
property, not a delivery guarantee.

`Fanout.Send` logs each rejected event at the point of failure, naming
the sink and the article. `harvest.deliver` does not repeat that detail:
it returns a summary ("N of M articles failed delivery") rather than
joining every article's own error, so a sustained sink outage produces
one bounded line per source in `RunOnce`'s log, not an unbounded one
repeating what `Fanout` already logged.

## Concurrency

Three settings bound how much of this happens at once, and they bound
different things:

- **`SOURCE_CONCURRENCY`** (default 8) bounds how many sources run their
  five stages at once, via `errgroup.Group.SetLimit` in
  `Harvester.RunOnce`.
- **`ARTICLE_CONCURRENCY`** (default 10) bounds how many article pages one
  source's enrich stage fetches at once, via `errgroup.Group.SetLimit` in
  `Scraper.Enrich`.
- **`DELIVERY_CONCURRENCY`** (default 8) bounds how many of one source's
  articles are delivered at once, via `errgroup.Group.SetLimit` in
  `Harvester.deliver`. It is what gives a batching sink something to batch:
  Pub/Sub's client bundles messages that are in flight together, so at
  `DELIVERY_CONCURRENCY=1` its bundler only ever coalesces what
  overlapping *sources* happen to contribute.
- **`PER_HOST_RPS`** (default 2) is enforced by `httpx.HostLimiter`, keyed
  on hostname rather than on source.

The host key matters specifically for the scrape stage, not the sitemap
fetch: the 26 configured sources span 26 distinct hostnames, so their
sitemap fetches never contend with each other regardless of how the limiter
is keyed. But one source's enrich stage issues hundreds of article
requests that all resolve to that source's single host — that is where a
per-host limiter actually does something. Keying on source instead would
also let two sources belonging to the same publisher (if ever configured)
run unthrottled against their shared host.

`SOURCE_CONCURRENCY` and `DELIVERY_CONCURRENCY` multiply: at the defaults,
up to 8 sources are each delivering up to 8 articles, so a single sink can
see 64 concurrent `Send` calls. Nothing in the harvester rate-limits
delivery — `PER_HOST_RPS` governs `httpx.Client`, which the sinks do not
use, because a sink's URL is operator configuration rather than something
publisher XML can influence. An operator pointing the `http` sink at a
rate-limited endpoint (a Slack incoming webhook, say) should lower
`DELIVERY_CONCURRENCY` rather than `ARTICLE_CONCURRENCY`, which bounds a
different stage. A rejected delivery is not lost: the article is simply
not marked, so the next crawl retries it.

**`errgroup.WithContext` is deliberately not used at the source level.**
`Harvester.RunOnce` uses a plain `errgroup.Group` with `SetLimit`, not
`errgroup.WithContext`. `WithContext` cancels every goroutine's context on
the first error returned by any of them — exactly wrong here, since one
publisher answering 403 or timing out must not cancel the other
twenty-five in-flight crawls. Each source's error is instead collected
under a mutex and joined into the value `RunOnce` returns, after every
source has had its chance to finish. (`errgroup.WithContext` *is* used one
level down, inside `Scraper.Enrich`, where cancelling the remaining
in-flight article fetches for a source whose context has already been
cancelled by the caller is the right behaviour — that is a different
scope than cancelling sibling sources.) `Harvester.deliver` follows the
source-level rule rather than the enrich-stage one, and for the same
reason: one article's sink failure must not cancel the deliveries in
flight beside it. A context cancelled part-way through a batch stops new
deliveries from being dispatched, and the articles never attempted are not
counted as delivery failures — otherwise every shutdown would report one.

## Failure handling

| Stage | Failure | Consequence |
|---|---|---|
| Fetch | Sitemap request errors, times out, or returns non-200 | That source is skipped for this crawl; every other source continues unaffected. |
| Dedupe | The `Unseen` call errors (e.g. bbolt I/O error) | That source is skipped for this crawl; nothing is marked, nothing is delivered. |
| Enrich | An article's page fetch or parse fails | The article keeps the title (and no description/image) the sitemap already provided; it is still delivered. |
| Deliver | One sink rejects an event, others accept | The article is **not** marked delivered; it is redelivered to every sink — including the ones that already accepted it — on the next crawl. |
| Mark | The `Mark` call errors after successful delivery | The already-delivered article is redelivered next crawl, because it was never recorded as seen. This is the safe direction: a redundant delivery, not a lost one. |

In every row, the failure is scoped to the smallest unit that can absorb
it — one article's metadata, one source's crawl — rather than aborting the
whole process. `Harvester.Run` logs a crawl's joined failures and continues
to the next tick; it never exits because sources failed.

`cmd/harvester -once` is the one place a crawl failure can end the
process: it exits non-zero only when `harvest.FailedSources` reports that
every configured source failed, not on a partial failure — see
[the README](../README.md#quickstart) for why. `FailedSources` exists
solely to make that distinction from the error `RunOnce` already returns,
without changing `RunOnce`'s signature or `Run`'s behaviour.

## Interface inventory

There are **seven** interfaces in the codebase, each declared at the
package that consumes it rather than at the package that implements it.
They fall into three groups, by why each one exists.

**One exported interface with several runtime implementations:**

| Interface | Declared in | Why it exists |
|---|---|---|
| `sink.Sink` | `internal/sink/sink.go`, next to `Fanout` | Five concrete types (`Log`, `HTTP`, `SQS`, `SNS`, `PubSub`) are selected from `configs/sinks.yaml` at startup, and `Fanout` has to hold a slice of whichever were configured. This is the one interface exported for genuine runtime polymorphism. |

**Five consumer-declared test seams**, each with exactly one production
implementation, existing solely so the consuming package's tests can
substitute a fake instead of doing real I/O:

| Interface | Declared in | Production implementation |
|---|---|---|
| `harvest.Fetcher` | `internal/harvest/harvest.go` | `source.Sitemap` |
| `harvest.Enricher` | `internal/harvest/harvest.go` | `enrich.Scraper` |
| `harvest.Deduper` | `internal/harvest/harvest.go` | `dedupe.Bolt` (and `dedupe.Noop` for `DEDUPE_BACKEND=none` — two implementations, which is itself a reason beyond the test seam) |
| `sink.sqsAPI` | `internal/sink/sqs.go` | `*sqs.Client` from `aws-sdk-go-v2`, narrowed to the one `SendMessage` call the sink makes |
| `sink.snsAPI` | `internal/sink/sns.go` | `*sns.Client` from `aws-sdk-go-v2`, narrowed to the one `Publish` call the sink makes |

`harvest.Fetcher`, `harvest.Enricher` and `harvest.Deduper` are
capitalised — Go-exported — even though nothing outside `internal/` can
see them, because `harvest.New` takes an exported `Deps` struct and an
exported struct field cannot reference an unexported interface type from
another package. `sqsAPI` and `snsAPI` carry no such constraint and are
genuinely unexported, package-private to `internal/sink`. All five are
still declared at their sole consumer and still invisible outside the
module.

**One generic constraint:**

| Interface | Declared in | Why it exists |
|---|---|---|
| `config.Item` | `internal/config/file.go` | The type-parameter bound for `ValidateList[T Item]` (`Ident() string; Validate() error`). Both `source.Config` and `sink.Config` satisfy it, so one generic loader validates both config files instead of the same loop written twice. |

The rule behind this list: an interface exists in this service only where
there are several real implementations, where a consumer needs a seam to
substitute a fake in a test, or where a shared algorithm needs a generic
constraint. Every type with exactly one production implementation and
none of those needs — the HTTP client, the config loader, the logger — is
a concrete type with exported methods, called directly.

## What is deliberately absent

- **No retry.** A failed fetch, scrape, or delivery is not retried within a
  crawl; the next scheduled crawl is the retry.
- **No outbox.** A partially-delivered event is redelivered wholesale next
  crawl rather than resumed only against the sinks that missed it — see
  [Delivery is at-least-once](../README.md#delivery-is-at-least-once).
- **No metrics.** There is no `/healthz`, no Prometheus endpoint, no pprof
  wiring. Progress and failures are visible only through the structured
  JSON logs on stdout.
- **No conditional GET.** Every crawl fetches every sitemap in full, even
  though `httpx.Response` already carries the `ETag` and `Last-Modified`
  validators that would make a conditional request possible.

These are not oversights. Each was considered and deliberately deferred
when the rewrite was scoped, so that the rewrite changed how the service
is built without also changing what it does. Conditional GET,
`<lastmod>`-based skipping and a `harvester discover` subcommand are the
next feature work, in that order, followed by metrics and an outbox with
retry and a dead-letter queue.
