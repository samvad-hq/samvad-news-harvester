# Harvester v2 — Design

**Status:** approved, not yet implemented
**Date:** 2026-09-06
**Scope:** full rewrite of the service internals. No new features.

---

## 1. Why

`samvad-news-harvester` works. It ran in production for nine months against 26 Indian
news sitemaps. This rewrite is not a rescue — the fetcher/sink split, the injected HTTP
client and the per-provider proxy are the right shapes and all survive. What does not
survive is the plumbing between them, which accreted rather than being designed.

An architecture review on 2026-09-06 found 21 issues. This document addresses the
correctness, throughput and design-debt items. Feature work (RSS was considered and
rejected on licensing grounds; conditional GET, discovery, clustering, metrics) is
explicitly out of scope and comes after.

Three constraints shape every decision below:

1. **No compatibility burden.** Samvad has been shut down. Nothing consumes the event
   JSON, no deployment reads the current env vars. We optimise for the right design.
2. **The goal is self-hostability.** Someone should clone this, run one command, and
   watch news events arrive — the way people run Langfuse or Hoppscotch. That makes
   clarity and documentation load-bearing, not optional.
3. **Go idiom is the tiebreaker.** Several of the current abstractions are Java habits
   in Go syntax. Where a rule from the Go proverbs or from
   `samber/cc-skills-golang` applies, it wins over local precedent.

---

## 2. What is wrong today

Grouped by kind, with the finding IDs used in the review.

### Correctness

| ID | Defect |
|----|--------|
| C1 | Both worker pools can deadlock on shutdown. The producer checks `ctx.Err()` then blocks on an unbuffered send; workers return on the same condition. If cancellation lands between the check and the send, every receiver is gone and the producer blocks forever, so `wg.Wait()` is never reached. |
| C2 | `Fanout.Publish` returns a success *count*, and the caller marks an article seen when the count is above zero. If SQS accepts and the webhook fails, the article is recorded as delivered and the webhook never sees it again. |
| C3 | Article IDs are `sha1(rawURL)`. Tracking parameters, trailing slashes, scheme and host case all produce different IDs for one article, so dedupe misses the case it exists to catch. |
| C5 | The no-providers path returns `ctx.Err()`, which `main` turns into exit code 1. A SIGTERM in a misconfigured deployment looks like a crash. |

### Throughput

| ID | Defect |
|----|--------|
| T1 | `SeenArticle` uses `db.Update` — a write transaction — to answer a read, so it can lazily delete expired keys. bbolt allows one writer at a time, so the whole crawl serialises on that lock. |
| T2 | `MarkArticle` opens its own transaction per article. `jagran` publishes 2500 URLs per sitemap; that is 2500 transactions and 2500 fsyncs on the lock T1 is already contending for. |
| T3 | Ten scrape workers share one `time.Ticker`, so the effective rate is one request per `request_delay_ms` for the whole pool — the workers only take turns. Meanwhile eight sources run in parallel with no global cap, and nothing bounds the scrape stage, where hundreds of article URLs from one source all hit that source's single host. |

### Design debt

| ID | Defect |
|----|--------|
| D1 | `pkg/providers` and `pkg/publishers` import `internal/domain`, so nothing outside the module can compile against them. `pkg/` promises an API it cannot deliver. |
| D2 | `Logger` is a five-method interface of `XxxObj(msg, key string, obj interface{})`. Every call site allocates a `map[string]any`, zap's typed fields are discarded, no method takes a `context.Context`, and a mutable package-level global backs free functions. |
| D3 | Viper reads no configuration file. It resolves ten environment variables and drags in afero, mapstructure, pflag, cast, toml, ini and fsnotify to do it. |
| D4 | `providers.LoadRegistry` and `publishers.LoadRegistry` are the same loader written twice: open, sniff extension, decode YAML or JSON, sanitise, validate, reject duplicate IDs. |
| D5 | Errors are logged *and* returned. `Process` returns an error, `worker` logs it and forwards it, and the joined error is logged again at the top of the loop. |
| D6 | Provider configuration is `map[string]any` read through `ConfigString(cfg, key, fallback)`. A typo in `user_agent` is a silent empty header at runtime instead of a load error. |
| D7 | `internal/scheduler` and `internal/util` are unreferenced nine-line stubs. Nil-receiver guards appear on nearly every method despite constructors guaranteeing the invariant. |
| D8 | Three registries carry `sync.RWMutex` but are built once during startup and never written again. |
| D9 | `Store` methods take no `context.Context`. `googleNewsFetcher.Fetch` rejects any provider whose `Type` is not `google_news_sitemap`, which breaks the registry's own ID-override path. |

### Interface design

This deserves its own section because it is the pervasive one.

**Four files named `interfaces.go`.** The name is the symptom: Go places an interface
next to the code that consumes it, so a file that collects them guarantees they sit
away from their consumers.

**Interfaces declared at the producer** — `providers.Fetcher`, `providers.FetcherRegistry`,
`publishers.Publisher`, `publishers.Registry`, `storage.Store`, `httpclient.Client`,
`httpclient.Response`, `logger.Logger`. The one declared correctly is
`crawler.ArticleDeduper`, at its consumer — and it duplicates `storage.Store`, so the
codebase has two interfaces for one concept and the accidental one is the correct one.

**Constructors returning interfaces**, which hides every other method of the concrete
type from callers: `NewFetcherRegistry`, `NewTypeFetcherRegistry`, `DefaultFetcherRegistry`,
`NewGoogleNewsFetcher`, `DefaultHTTPClient`, `publishers.NewRegistry`, `DefaultRegistry`,
`storage.NewStore`, and every `newXxxPublisher` and `newXxxSender`.

**Premature abstraction.** `Logger`, `Store`, `Client`, `Response`, `FetcherRegistry`
and `publishers.Registry` each have exactly one implementation. Per *"don't design with
interfaces, discover them"*, each is pure indirection. `Publisher` is the genuine
exception: HTTP, SQS, SNS and Pub/Sub are four implementations selected at runtime from
configuration.

**Stuttering at the call site** — `providers.Provider`, `publishers.PublisherConfig`,
`publishers.QueuePublisherConfig`, `publishers.AWSSQSPublisherConfig`,
`publishers.HTTPPublisherConfig`, and `providers.HTTPClient`, which is a bare alias of
`httpclient.Client` introduced "for clarity within providers".

**Plural package names.** Go convention is singular: `net/url`, not `net/urls`.

**Two things named Registry in one package** — `publishers.Registry` holds builders,
`publishers.ConfigRegistry` holds configuration.

**A method name that lies.** `Fetcher.ID()` returns the provider *type*
(`googleNewsFetcher.ID()` returns `"google_news_sitemap"`). That mismatch is the direct
cause of D9's second half.

**`interface{}` rather than `any`** throughout, on Go 1.24.

---

## 3. Approaches considered

**A — Discover, don't design.** Delete the interfaces that have one implementation.
Concrete structs everywhere; the pipeline declares small unexported interfaces for the
handful of things it genuinely needs to vary. Registries become plain maps built at
startup.

**B — Keep the exported contracts, fix placement and naming.** Retain `Fetcher` and
`Publisher` as exported extension points, move them to their consumers, rename packages,
return concrete types from constructors.

**C — Generics-first.** One `registry[T]` serving both configuration loading and builder
lookup.

**Chosen: A, with one exception.** The five interfaces with a single implementation are
indirection with no payoff. `Sink` (the renamed `Publisher`) stays exported because five
implementations are selected at runtime — that abstraction was discovered, not guessed.

C is rejected as an abstraction, but its useful half is kept: D4's duplicated loader
becomes a generic `LoadList[T]` *helper*, which is a function, not a type hierarchy.

---

## 4. Package layout

`pkg/` is removed entirely. This is a service you run, not a library you import — the
same posture as Langfuse and Hoppscotch. Contributors extend it by editing the repository,
which is what Go requires anyway since plugins need recompilation. Nothing is lost and the
API-stability burden disappears.

```
cmd/harvester/          main, flags, signal context, wiring
internal/
  news/       Article, Event, CanonicalURL, ID        no dependencies
  config/     Config, Load, Validate                  environment only
  httpx/      Client, Response, ProxyCache            net/http
  source/     Config, Sitemap, LoadFile               was pkg/providers
  enrich/     Scraper                                 was crawler/scraper.go
  dedupe/     Bolt, Noop                              was internal/storage
  sink/       Config, Sink, Fanout, HTTP, SQS,        was pkg/publishers
              SNS, PubSub, Log
  harvest/    Harvester                               was crawler + app
```

Deleted: `internal/util`, `internal/scheduler`, `internal/crawler`, `internal/logger`,
`internal/domain`, `internal/storage`, `pkg/`, and all four `interfaces.go` files.

### Why "source" and "sink"

"Publisher" is overloaded in this codebase. The Hindu is a news publisher; SQS is an
event publisher. Renaming the output side to `sink` removes a real ambiguity in a
project whose entire domain is publishers, and pairs naturally with `source`.

### Why "news" rather than "domain"

`news.Article` names the thing. `domain.Article` names the architectural layer, which
the reader already knows. Both are conventional; the first is more specific.

Every resulting call site is checked for stutter: `source.Config`, `sink.HTTP`,
`dedupe.Bolt`, `news.Article`, `httpx.Client`, `harvest.New`.

---

## 5. Interfaces after

Four interfaces total — three unexported, one exported — each declared where it is
consumed, each with one or two methods.

```go
// internal/harvest — the pipeline declares what it needs. These three exist as test
// seams: each has exactly one production implementation, but the pipeline is the one
// component that cannot be tested without substituting all of them.
type fetcher interface {
	Fetch(ctx context.Context, src source.Config) ([]news.Article, error)
}

type enricher interface {
	Enrich(ctx context.Context, src source.Config, arts []news.Article) []news.Article
}

type deduper interface {
	Unseen(ctx context.Context, ids []string) ([]string, error)
	Mark(ctx context.Context, ids []string) error
}
```

```go
// internal/sink — declared next to Fanout, which is the consumer.
type Sink interface {
	Name() string
	Send(ctx context.Context, evt news.Event) error
}
```

`Sink` is exported because implementations are selected at runtime from configuration.

**Amendment made during planning:** the three pipeline interfaces are exported too,
as `harvest.Fetcher`, `harvest.Enricher` and `harvest.Deduper`. `harvest.New` takes an
exported `Deps` struct, and an exported struct field cannot have an unexported interface
type that callers outside the package need to satisfy. The rule that matters is
unchanged — they are declared at the consumer, they hold one or two methods, and nothing
outside `internal/` can see them. Exporting them within `internal/` costs nothing.

Constructors return concrete types:
`source.NewSitemap(...) *source.Sitemap`, `dedupe.OpenBolt(...) (*dedupe.Bolt, error)`,
`sink.NewHTTP(...) (*sink.HTTP, error)`.

### The extension contract

`Fetcher` is deliberately **not** a Go type. Adding a source type means writing a
concrete struct with a `Fetch` method and adding one line to a map — Go's structural
typing means an implementer never imports an interface to satisfy one. The contract is
documented in the README; it does not need to exist in the type system to be real.

Removed: `Logger`, `Store`, `httpclient.Client`, `httpclient.Response`,
`FetcherRegistry`, `publishers.Registry`, `Builder`, `ArticleScraper`, `EventPublisher`,
`ArticleDeduper`, and the `HTTPClient` alias. Both registries become plain maps built
once at startup, with no mutex, because nothing writes to them after construction (D8).

---

## 6. The pipeline

```go
func (h *Harvester) runOnce(ctx context.Context, sources []source.Config) error {
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(h.cfg.SourceConcurrency) // default 8
	for _, src := range sources {
		g.Go(func() error { return h.runSource(ctx, src) })
	}
	return g.Wait()
}
```

`runSource` runs five stages in order:

1. **fetch** — one HTTP GET, sitemap index followed if present
2. **identify** — `news.CanonicalURL` then SHA-256, producing the article ID
3. **dedupe** — one `Unseen` call for the whole batch, one read transaction
4. **enrich** — per-article metadata scrape, host-rate-limited
5. **deliver** — fan out to sinks, then one `Mark` call for the whole batch

C1 is fixed structurally. There are no hand-rolled channels, so there is no send that
can block after its receivers have exited. `errgroup.WithContext` also cancels siblings
on the first error, which the current code cannot do.

### Rate limiting

Replaced with `golang.org/x/time/rate.Limiter` keyed by **host**, shared process-wide,
plus a global in-flight cap. The current model gives a shared ticker to ten workers, so
the pool accomplishes nothing.

Keying on host rather than on source matters for the scrape stage, not the fetch stage:
the real configuration has 26 sources across 26 distinct hostnames, so no two sitemap
fetches contend, but a single source's crawl issues hundreds of article requests that
all resolve to that source's one host. A per-source limit would let two sources of the
same publisher run unthrottled against it; a per-host limit is what actually bounds the
request rate any one origin sees.

---

## 7. Fan-out and delivery semantics (C2)

```go
type Result struct {
	Delivered []string          // sink names that accepted
	Failed    map[string]error  // sink name -> error
}

func (f *Fanout) Send(ctx context.Context, evt news.Event) Result
```

An article is marked seen only when `Failed` is empty.

This trades a duplicate delivery to the sinks that succeeded for never silently losing
an article. At-least-once is the correct default for a news pipeline, and it is the
honest one: today's behaviour claims exactly-once and delivers neither. The duplicate is
eliminated later by the outbox (R10), which is out of scope here.

**This must be documented in the README**, not left implicit. Consumers need to know
they may see an event twice and should key on `article.id`.

---

## 8. Component decisions

### news — identity (C3)

```go
func CanonicalURL(raw string) string  // normalise
func ID(canonicalURL string) string   // sha256, hex
```

Canonicalisation: lowercase scheme and host, drop the default port, remove `utm_*`,
`fbclid`, `gclid` and `ref`, sort the remaining query parameters, drop the fragment,
strip a trailing slash. SHA-256 replaces SHA-1, which removes the `//nolint:gosec`
suppression.

The canonical URL is stored on the `Article`, not just hashed and discarded — consumers
that deduplicate downstream need the same normalisation.

### config — no viper (D3)

A plain struct, `os.Getenv` with typed helpers, and a `Validate() error` method called
during `Load`. `godotenv` stays: it is one small dependency with no transitive
dependencies, and `.env` support matters for self-hosting. Viper goes, taking roughly
twelve transitive dependencies with it.

`Validate` is also what the future `harvester validate` subcommand calls, so
misconfiguration is caught before a crash loop rather than by one.

### logging — slog (D2)

`*slog.Logger`, JSON handler on stdout, level from configuration, passed explicitly
through constructors. Per-source context via `slog.With("source", id)`. No interface, no
package-level global, no `map[string]any` payloads. zap is removed.

`slog` has been standard library since Go 1.21 and takes a `context.Context` on every
method, which is what makes trace correlation possible later without threading a new
parameter through every function.

### httpx — net/http (replaces resty)

```go
type Response struct {
	Status       int
	Body         []byte   // capped by io.LimitReader
	ETag         string
	LastModified string
}
```

resty's `Body()` forces the entire response into memory before any cap can apply — the
current code reads a 1.3 MB HTML error page from a 404ing source, then truncates. A
concrete struct over `net/http` applies the cap at read time, removes a dependency, and
carries the cache validators that make conditional GET (R4) a small change later rather
than a refactor.

`ProxyCache` survives essentially unchanged; it is one of the good parts.

### source — typed configuration (D6, D9)

`Config` gains a typed `Headers` struct in place of `map[string]any` plus `ConfigString`.
Unknown keys become load-time errors instead of silent runtime defaults.

`Fetcher.ID()` is deleted along with the interface. Source types are keyed in a map by
their type string, so nothing needs to report its own identity and the type-mismatch
guard that broke ID-registered sources disappears with it.

### dedupe — batch and context (T1, T2, D9)

```go
type Bolt struct{ ... }

func (b *Bolt) Unseen(ctx context.Context, ids []string) ([]string, error)
func (b *Bolt) Mark(ctx context.Context, ids []string) error
func (b *Bolt) Close() error
```

`Unseen` uses `db.View`. An expired key reads as unseen and the periodic sweeper reclaims
it, so the read path never takes the write lock. `Mark` writes the whole batch in one
transaction: one fsync per source rather than 2500 for `jagran`.

### errors (D5)

Errors are returned or logged, never both. Only the top of the crawl loop logs. Sentinel
errors are defined where callers need to branch. Error strings stay lowercase and
unpunctuated per Go convention.

---

## 9. Testing

- Table-driven tests with `testify/require`.
- `httptest.Server` for every fetch path — no network in unit tests.
- **Recorded fixtures** captured from the 26 real configured sitemaps, so that a
  publisher changing their XML shape breaks CI rather than production. This is the
  failure mode that actually pages you and nothing currently catches it.
- `go test -race ./...` in CI from the first pull request.
- `.golangci.yml` committed and the CI action pinned to a version. Today it runs
  `latest` with no configuration, so a new lint release can break `main` with no local
  reproduction.

Coverage targets: `news` (canonicalisation and hashing) and `dedupe` are pure logic and
should be near-exhaustive. `harvest` is tested through fakes for the three interfaces.

---

## 10. Documentation

Non-negotiable, and part of each pull request rather than a final pass:

- **Package doc comment** on every package, saying what it does and what it depends on.
- **README** rewritten around the layout, the extension contract for new source types
  and sinks, and the at-least-once delivery semantics from section 7.
- **`docs/architecture.md`** — the five stages, the concurrency model, where the
  bounded queues and rate limits sit.
- **`docs/configuration.md`** — every environment variable and every YAML field, with
  defaults and validation rules.
- **`CHANGELOG.md`** started at this rewrite, since the plan is tagged releases.
- Exported identifiers carry doc comments beginning with the identifier name.

---

## 11. Delivery

Work lands on `main`, each commit building and passing tests on its own so the history
stays bisectable. New packages are built alongside the old tree; a single cutover commit
rewires `cmd/harvester` and deletes what they replace.

**Amendment made during planning:** the six groups below became eleven commits in
[`harvester-v2-plan.md`](harvester-v2-plan.md). Group 4 splits because `source` and
`enrich` are separately reviewable, group 5 splits the local sinks from the cloud ones,
and documentation becomes its own commit rather than riding along with the cutover.

| # | Contents |
|---|----------|
| 1 | Tooling: `.golangci.yml`, CI with `-race`, testify, fixture capture script |
| 2 | `news`: types, canonical URL, hashing (C3). Pure and heavily tested. |
| 3 | `httpx`: net/http client, body cap, proxy cache, host limiter (T3) |
| 4 | `config`: environment parsing without viper, shared file loader (D3, D4) |
| 5 | `source`: typed config, sitemap parsing, fetcher, recorded fixtures (D6, D9) |
| 6 | `enrich`: errgroup scraper (C1) |
| 7 | `dedupe`: batch store on read transactions (T1, T2) |
| 8 | `sink`: contract, fan-out with per-sink results (C2), log and HTTP sinks |
| 9 | `sink`: cloud sinks and the builder |
| 10 | `harvest`: staged pipeline (C1, C5); old packages deleted |
| 11 | Documentation: README, architecture, configuration, CHANGELOG |

Commit 8 flips the sinks example to the log sink. The current example enables
GCP Pub/Sub, so the documented first run fails without Google credentials — which is the
single worst thing in the repository for a newcomer.

---

## 12. Explicitly out of scope

Deferred to feature work, after this lands:

- Conditional GET (R4), sitemap index `<lastmod>` skipping (R1), `harvester discover` (R2),
  the freshness ladder (R3)
- Prometheus metrics, `/healthz`, pprof (R9)
- Outbox with retry and dead-letter queue (R10)
- CLI subcommands beyond what wiring requires (R11)
- Dockerfile and compose (R12)
- Near-duplicate clustering (R5), shared-store dedupe (R14)
- RSS and Atom — considered and **rejected**: many publishers restrict feeds to personal,
  non-commercial use, whereas news sitemaps are published for crawler consumption and
  advertised from `robots.txt`.

The rewrite must not preclude any of these. In particular `httpx.Response` carries cache
validators for R4, and the per-source loop in section 6 is shaped so a per-source
schedule (R3) replaces the single ticker without touching the stages.
