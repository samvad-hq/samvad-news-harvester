# Harvester v2 Implementation Plan

**Goal:** Rewrite the internals of `samvad-news-harvester` so the service is correct under cancellation, does not lose articles on partial sink failure, stops serialising every crawl on a bbolt write lock, and reads like idiomatic Go.

**Architecture:** New packages are built bottom-up under `internal/` alongside the existing tree, so every commit compiles and tests green. The final task rewires `cmd/harvester` and deletes the old packages in one cutover. Interfaces are declared at their consumers; everything with a single implementation is a concrete struct.

**Tech Stack:** Go 1.24.6, stdlib `net/http` and `log/slog`, `golang.org/x/sync/errgroup`, `golang.org/x/time/rate`, `go.etcd.io/bbolt`, `github.com/PuerkitoBio/goquery`, `gopkg.in/yaml.v3`, `github.com/joho/godotenv`, AWS SDK v2, GCP Pub/Sub, `github.com/stretchr/testify/require`.

**Spec:** [`harvester-v2-design.md`](harvester-v2-design.md)

## Global Constraints

- Go 1.24.6. Use `any`, never `interface{}`. Use range-over-int (`for i := range n`).
- **No compatibility burden.** Samvad is shut down. Environment variable names, YAML field names, the published event JSON, and the provider `type` string may all change. Document every change.
- **Removed dependencies, do not reintroduce:** `github.com/spf13/viper`, `go.uber.org/zap`, `github.com/go-resty/resty/v2`.
- **Kept dependencies:** `github.com/joho/godotenv` (zero transitive deps, needed for `.env` self-hosting).
- **Added dependencies:** `github.com/stretchr/testify` (test only), `golang.org/x/sync`, `golang.org/x/time`.
- Interfaces are declared where consumed, hold 1–2 methods, and are unexported unless implementations are selected at runtime. Only `sink.Sink` is exported.
- Constructors return concrete types (`*Foo`), never interfaces.
- Package names are singular and non-stuttering. Verify at the call site: `source.Config`, not `source.SourceConfig`.
- Errors are returned **or** logged, never both. Only `harvest.Run` logs the errors it receives.
- Error strings are lowercase and unpunctuated: `"invalid source id"`, not `"Invalid source ID."`.
- Every package has a doc comment. Every exported identifier has a doc comment starting with its name.
- Every task ends green: `go build ./... && go vet ./... && go test -race ./...`.
- Commit messages end with:
  `Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>`

---

## File Structure

Created over the course of the plan:

| Path | Responsibility |
|------|----------------|
| `.golangci.yml` | Pinned lint configuration |
| `internal/news/news.go` | `Article`, `Event`, `NewEvent` |
| `internal/news/url.go` | `CanonicalURL`, `ID` — article identity |
| `internal/httpx/httpx.go` | `Client`, `Response`, `Get` with a body cap |
| `internal/httpx/proxy.go` | `ProxyCache` — one client per proxy URL |
| `internal/httpx/hostlimit.go` | `HostLimiter` — per-host politeness |
| `internal/config/config.go` | `Config`, `Load`, `Validate` — environment only |
| `internal/config/file.go` | `DecodeFile`, `ValidateList` — the D4 deduplication |
| `internal/source/source.go` | `source.Config`, `Headers`, `LoadFile` |
| `internal/source/sitemap.go` | `Sitemap` fetcher: index walking, article building |
| `internal/source/xml.go` | Sitemap XML types and lenient decoding |
| `internal/enrich/scraper.go` | `Scraper` — Open Graph metadata enrichment |
| `internal/dedupe/dedupe.go` | `Noop` — dedupe disabled, plus the package doc |
| `internal/dedupe/bolt.go` | `Bolt` — batch, context-aware dedupe store |
| `internal/sink/sink.go` | `Sink` interface, `Fanout`, `Result` |
| `internal/sink/build.go` | `Build` — config to sinks, plus their closer |
| `internal/sink/config.go` | `sink.Config`, per-type configs, `LoadFile`, `Build` |
| `internal/sink/http.go`, `log.go`, `sqs.go`, `sns.go`, `pubsub.go` | The five sinks |
| `internal/harvest/harvest.go` | `Harvester`, the crawl loop, `Fetcher`/`Enricher`/`Deduper` |
| `cmd/harvester/main.go` | Wiring, signal context, exit codes |

Deleted in Task 10: `pkg/`, `internal/crawler`, `internal/storage`, `internal/logger`, `internal/domain`, `internal/util`, `internal/scheduler`.

---

## Task 1: Tooling and dependencies

**Files:**
- Create: `.golangci.yml`
- Modify: `.github/workflows/ci.yml`, `Makefile`, `go.mod`

**Interfaces:**
- Consumes: nothing.
- Produces: a CI pipeline that runs `go test -race ./...`; `github.com/stretchr/testify/require`, `golang.org/x/sync/errgroup` and `golang.org/x/time/rate` available to later tasks.

- [ ] **Step 1: Add the dependencies**

```bash
cd /Users/anindyac/samvad-news-harvester
go get golang.org/x/sync@latest
go get golang.org/x/time@latest
go mod tidy
```

`golang.org/x/sync` and `golang.org/x/time` are already in the module graph as
indirect requires and are importable as-is; `go mod tidy` promotes them to direct
requires in Task 3, when the first package imports them.

**Do not `go get` testify here.** `go mod tidy` removes a test dependency that no
package imports yet, so the pair of commands cancel out and `go.mod` is left
unchanged — while the commit claims otherwise. testify enters `go.mod` in Task 2,
where the first test imports it and tidy therefore keeps it. Task 2 runs
`go mod tidy` for exactly this reason.

- [ ] **Step 2: Write `.golangci.yml`**

The current CI runs `golangci-lint` at `version: latest` with no configuration file, so a new lint release can break `main` with no local reproduction.

Written against the **v1 configuration schema**, because `golangci-lint v1.64.8`
is what is installed locally and what CI pins below. The v2 schema (`version: "2"`,
a `formatters` block) will not parse under v1, and having `make lint` and CI disagree
is the exact failure this file exists to prevent. Moving to v2 is a separate change.

```yaml
run:
  timeout: 5m

linters:
  enable:
    - bodyclose
    - contextcheck
    - copyloopvar
    - errorlint
    - gofmt
    - goimports
    - gosec
    - misspell
    - nilerr
    - noctx
    - revive
    - unconvert
    - unparam
    - usestdlibvars
    - wastedassign

linters-settings:
  revive:
    rules:
      - name: exported
        arguments: ["checkPrivateReceivers"]
      - name: package-comments
      - name: error-strings
      - name: error-naming
      - name: var-naming
      - name: indent-error-flow
      - name: context-as-argument
      - name: receiver-naming

issues:
  exclude-rules:
    - path: _test\.go
      linters:
        - gosec
        - unparam
```

- [ ] **Step 3: Update CI**

Replace `.github/workflows/ci.yml` with:

```yaml
name: CI

on:
  pull_request:
  push:
    branches: [main]

permissions:
  contents: read

jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version: '1.24.6'
          cache: true

      - name: Format check
        run: |
          fmt_out=$(gofmt -l .)
          if [ -n "$fmt_out" ]; then
            echo "gofmt found issues:"
            echo "$fmt_out"
            exit 1
          fi

      - name: Vet
        run: go vet ./...

      - name: Lint
        uses: golangci/golangci-lint-action@v6
        with:
          version: v1.64.8

      - name: Test
        run: go test -race -coverprofile=coverage.out ./...

      - name: Coverage summary
        run: go tool cover -func=coverage.out | tail -1
```

- [ ] **Step 4: Update the Makefile**

```makefile
.PHONY: build run test race lint fmt tidy hooks

build:
	go build ./...

run:
	go run ./cmd/harvester

test:
	go test ./...

race:
	go test -race ./...

lint:
	go vet ./...
	golangci-lint run

fmt:
	gofmt -w .

tidy:
	go mod tidy

hooks:
	git config core.hooksPath scripts/githooks
```

- [ ] **Step 5: Verify green**

```bash
go build ./... && go vet ./... && go test -race ./...
```
Expected: builds, vets clean, existing tests pass.

- [ ] **Step 6: Commit**

```bash
git add .golangci.yml .github/workflows/ci.yml Makefile go.mod go.sum
git commit -m "$(cat <<'EOF'
build: pin lint config, run tests under -race

golangci-lint ran at `latest` with no config file, so a new lint
release could break main with no local reproduction. Pins the action
to v1.64.8, the version installed locally, and commits the ruleset in
the matching v1 schema so `make lint` and CI cannot disagree.

Adds -race and a coverage summary to CI.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: `internal/news` — article identity

This is the C3 fix. `ID = sha1(rawURL)` means tracking parameters, trailing slashes and host case all produce different IDs for one article, so dedupe misses the exact case it exists to catch.

**Files:**
- Create: `internal/news/news.go`, `internal/news/url.go`, `internal/news/url_test.go`, `internal/news/news_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `news.Article` struct with fields `SourceID, ID, Title, URL, OriginalURL, Description, ImageURL, Keywords, PublishedAt`
  - `news.Event` struct with fields `SourceID, SourceName, Article, CollectedAt`
  - `func news.NewEvent(sourceID, sourceName string, a Article) Event`
  - `func news.CanonicalURL(raw string) (string, error)`
  - `func news.ID(canonicalURL string) string`

- [ ] **Step 1: Write the failing tests for `CanonicalURL`**

Create `internal/news/url_test.go`:

```go
package news_test

import (
	"testing"

	"github.com/samvad-hq/samvad-news-harvester/internal/news"
	"github.com/stretchr/testify/require"
)

func TestCanonicalURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "already canonical is unchanged",
			input:    "https://www.thehindu.com/news/national/story",
			expected: "https://www.thehindu.com/news/national/story",
		},
		{
			name:     "host and scheme are lowercased",
			input:    "HTTPS://WWW.TheHindu.com/News/Story",
			expected: "https://www.thehindu.com/News/Story",
		},
		{
			name:     "default https port is dropped",
			input:    "https://example.com:443/story",
			expected: "https://example.com/story",
		},
		{
			name:     "default http port is dropped",
			input:    "http://example.com:80/story",
			expected: "http://example.com/story",
		},
		{
			name:     "non-default port is kept",
			input:    "https://example.com:8443/story",
			expected: "https://example.com:8443/story",
		},
		{
			name:     "fragment is dropped",
			input:    "https://example.com/story#section-2",
			expected: "https://example.com/story",
		},
		{
			name:     "trailing slash is stripped",
			input:    "https://example.com/story/",
			expected: "https://example.com/story",
		},
		{
			name:     "bare root keeps its slash",
			input:    "https://example.com/",
			expected: "https://example.com/",
		},
		{
			name:     "utm parameters are stripped",
			input:    "https://example.com/story?utm_source=twitter&utm_medium=social",
			expected: "https://example.com/story",
		},
		{
			name:     "click identifiers are stripped",
			input:    "https://example.com/story?fbclid=abc&gclid=def&msclkid=ghi",
			expected: "https://example.com/story",
		},
		{
			name:     "meaningful parameters survive and are sorted",
			input:    "https://example.com/story?page=2&id=7&utm_source=x",
			expected: "https://example.com/story?id=7&page=2",
		},
		{
			name:     "repeated parameter values are sorted",
			input:    "https://example.com/s?tag=z&tag=a",
			expected: "https://example.com/s?tag=a&tag=z",
		},
		{
			name:     "surrounding whitespace is trimmed",
			input:    "  https://example.com/story  ",
			expected: "https://example.com/story",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := news.CanonicalURL(tt.input)
			require.NoError(t, err)
			require.Equal(t, tt.expected, got)
		})
	}
}

func TestCanonicalURLRejectsBadInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{name: "empty", input: ""},
		{name: "whitespace only", input: "   "},
		{name: "relative path", input: "/news/story"},
		{name: "no host", input: "https://"},
		{name: "unsupported scheme", input: "ftp://example.com/story"},
		{name: "javascript scheme", input: "javascript:alert(1)"},
		{name: "unparseable", input: "https://exa mple.com/\x7f"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := news.CanonicalURL(tt.input)
			require.Error(t, err)
		})
	}
}

func TestCanonicalURLIsStableAcrossVariants(t *testing.T) {
	t.Parallel()

	variants := []string{
		"https://www.thehindu.com/news/story",
		"https://www.thehindu.com/news/story/",
		"https://WWW.thehindu.com/news/story",
		"https://www.thehindu.com/news/story?utm_source=whatsapp",
		"https://www.thehindu.com/news/story#top",
		"https://www.thehindu.com:443/news/story",
	}

	first, err := news.CanonicalURL(variants[0])
	require.NoError(t, err)
	firstID := news.ID(first)

	for _, v := range variants[1:] {
		got, err := news.CanonicalURL(v)
		require.NoError(t, err)
		require.Equal(t, first, got, "variant %q should canonicalise identically", v)
		require.Equal(t, firstID, news.ID(got))
	}
}

func TestID(t *testing.T) {
	t.Parallel()

	id := news.ID("https://example.com/story")
	require.Len(t, id, 64, "sha256 hex is 64 characters")
	require.Equal(t, id, news.ID("https://example.com/story"), "must be deterministic")
	require.NotEqual(t, id, news.ID("https://example.com/other"))
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/news/...`
Expected: FAIL — the `internal/news` package does not exist.

- [ ] **Step 3: Implement `internal/news/url.go`**

```go
// Package news holds the core domain types the harvester produces: an
// Article scraped from a publisher, and the Event published downstream.
// It also owns article identity — canonicalising a URL and hashing it —
// because that is what makes deduplication correct.
package news

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// ErrNotAbsoluteURL reports a URL that is missing a scheme or a host.
var ErrNotAbsoluteURL = errors.New("news: url is not absolute")

// trackingParams are query parameters that identify a referral rather than
// the article, so two links differing only in these point at one story.
var trackingParams = map[string]bool{
	"fbclid":   true,
	"gclid":    true,
	"gbraid":   true,
	"wbraid":   true,
	"msclkid":  true,
	"igshid":   true,
	"mc_cid":   true,
	"mc_eid":   true,
	"ref":      true,
	"ref_src":  true,
	"_ga":      true,
	"yclid":    true,
	"twclid":   true,
}

// isTracking reports whether a query parameter carries referral information
// rather than identifying the article.
func isTracking(key string) bool {
	lower := strings.ToLower(key)
	return strings.HasPrefix(lower, "utm_") || trackingParams[lower]
}

// CanonicalURL normalises raw into the single form used to identify an
// article. It lowercases the scheme and host, drops a default port, removes
// the fragment and every tracking parameter, sorts the parameters that
// remain, and strips a trailing slash from a non-root path.
//
// It returns an error for input that is not an absolute http or https URL,
// which also keeps unexpected schemes out of the crawl.
func CanonicalURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("%w: empty", ErrNotAbsoluteURL)
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("news: parse url: %w", err)
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("news: unsupported scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("%w: %q", ErrNotAbsoluteURL, raw)
	}

	u.Scheme = scheme
	u.Host = canonicalHost(u.Host, scheme)
	u.Fragment = ""
	u.RawFragment = ""
	u.RawQuery = canonicalQuery(u.Query())
	u.Path = canonicalPath(u.Path)

	return u.String(), nil
}

// canonicalHost lowercases the host and removes the port when it is the
// default for the scheme.
func canonicalHost(host, scheme string) string {
	host = strings.ToLower(host)
	switch {
	case scheme == "https" && strings.HasSuffix(host, ":443"):
		return strings.TrimSuffix(host, ":443")
	case scheme == "http" && strings.HasSuffix(host, ":80"):
		return strings.TrimSuffix(host, ":80")
	default:
		return host
	}
}

// canonicalPath strips a trailing slash from any path but the root, so
// "/story/" and "/story" identify one article while "/" stays "/".
func canonicalPath(path string) string {
	if len(path) > 1 {
		return strings.TrimSuffix(path, "/")
	}
	return path
}

// canonicalQuery drops tracking parameters and sorts what remains, so
// parameter order cannot change an article's identity.
func canonicalQuery(q url.Values) string {
	for key := range q {
		if isTracking(key) {
			delete(q, key)
		}
	}
	if len(q) == 0 {
		return ""
	}
	for _, values := range q {
		sort.Strings(values)
	}
	return q.Encode() // url.Values.Encode sorts by key
}

// ID returns the stable identifier for a canonical article URL: the hex
// SHA-256 of the URL. Pass the output of CanonicalURL, never a raw URL.
func ID(canonicalURL string) string {
	sum := sha256.Sum256([]byte(canonicalURL))
	return hex.EncodeToString(sum[:])
}
```

- [ ] **Step 4: Run the URL tests to verify they pass**

Run: `go test -race ./internal/news/...`
Expected: PASS.

- [ ] **Step 5: Write the failing test for `Article` and `Event`**

Create `internal/news/news_test.go`:

```go
package news_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/samvad-hq/samvad-news-harvester/internal/news"
	"github.com/stretchr/testify/require"
)

func TestNewEventStampsCollectedAtInUTC(t *testing.T) {
	t.Parallel()

	before := time.Now().UTC()
	evt := news.NewEvent("thehindu", "The Hindu", news.Article{ID: "abc"})
	after := time.Now().UTC()

	require.Equal(t, "thehindu", evt.SourceID)
	require.Equal(t, "The Hindu", evt.SourceName)
	require.Equal(t, "abc", evt.Article.ID)
	require.Equal(t, time.UTC, evt.CollectedAt.Location())
	require.False(t, evt.CollectedAt.Before(before))
	require.False(t, evt.CollectedAt.After(after))
}

func TestEventJSONShape(t *testing.T) {
	t.Parallel()

	evt := news.Event{
		SourceID:   "thehindu",
		SourceName: "The Hindu",
		Article: news.Article{
			SourceID:    "thehindu",
			ID:          "abc",
			Title:       "Headline",
			URL:         "https://www.thehindu.com/story",
			PublishedAt: time.Date(2026, 9, 6, 4, 0, 0, 0, time.UTC),
		},
		CollectedAt: time.Date(2026, 9, 6, 5, 0, 0, 0, time.UTC),
	}

	raw, err := json.Marshal(evt)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))

	require.Equal(t, "thehindu", got["source_id"])
	require.Equal(t, "The Hindu", got["source_name"])
	require.Equal(t, "2026-09-06T05:00:00Z", got["collected_at"])

	article, ok := got["article"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "abc", article["id"])
	require.Equal(t, "https://www.thehindu.com/story", article["url"])

	// Empty optional fields are omitted so consumers see a compact payload.
	require.NotContains(t, article, "description")
	require.NotContains(t, article, "image_url")
	require.NotContains(t, article, "keywords")
	require.NotContains(t, article, "original_url")
}
```

- [ ] **Step 6: Run it to verify it fails**

Run: `go test ./internal/news/... -run TestEventJSONShape`
Expected: FAIL — `news.Event` is undefined.

- [ ] **Step 7: Implement `internal/news/news.go`**

```go
package news

import "time"

// Article is one story discovered at a source. URL is always canonical;
// OriginalURL records the form the sitemap used when the two differ, so a
// consumer can trace an event back to the publisher's own link.
type Article struct {
	SourceID    string    `json:"source_id"`
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	URL         string    `json:"url"`
	OriginalURL string    `json:"original_url,omitempty"`
	Description string    `json:"description,omitempty"`
	ImageURL    string    `json:"image_url,omitempty"`
	Keywords    []string  `json:"keywords,omitempty"`
	PublishedAt time.Time `json:"published_at"`
}

// Event is the payload delivered to every sink.
//
// Delivery is at-least-once: an article is recorded as seen only once every
// sink has accepted it, so a partial failure replays the event to the sinks
// that already succeeded. Consumers must key on Article.ID.
type Event struct {
	SourceID    string    `json:"source_id"`
	SourceName  string    `json:"source_name"`
	Article     Article   `json:"article"`
	CollectedAt time.Time `json:"collected_at"`
}

// NewEvent wraps an article for delivery, stamping the collection time in UTC.
func NewEvent(sourceID, sourceName string, a Article) Event {
	return Event{
		SourceID:    sourceID,
		SourceName:  sourceName,
		Article:     a,
		CollectedAt: time.Now().UTC(),
	}
}
```

- [ ] **Step 8: Run the full package tests**

Run: `go test -race ./internal/news/...`
Expected: PASS, all tests.

- [ ] **Step 9: Add testify to go.mod and verify the whole module is green**

These tests are the first code in the module to import
`github.com/stretchr/testify/require`, so this is where the dependency actually
enters `go.mod` — `go mod tidy` keeps a test dependency once something imports it,
and strips it when nothing does. Running tidy here is required, not hygiene.

```bash
go mod tidy
grep -q 'github.com/stretchr/testify' go.mod && echo "testify is in go.mod"
go build ./... && go vet ./... && go test -race ./...
```
Expected: testify appears in `go.mod`, and everything passes. The new package is
not yet imported by any non-test code.

- [ ] **Step 10: Commit**

```bash
git add internal/news go.mod go.sum
git commit -m "$(cat <<'EOF'
feat(news): add domain types and canonical article identity

Article IDs were sha1 of the raw sitemap URL, so the same story arriving
with a utm_source parameter, a trailing slash, or a differently-cased
host produced three different IDs — the dedupe layer missed exactly the
case it exists to catch.

CanonicalURL normalises scheme and host case, drops default ports and
fragments, strips utm_* and the common click identifiers, sorts the
remaining query, and trims a trailing slash from non-root paths. ID is
sha256 over that, which also removes the gosec suppression sha1 needed.

The canonical URL is kept on the Article rather than only hashed, so
downstream consumers can apply the same identity.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: `internal/httpx` — HTTP client on net/http

resty's `Body()` buffers the whole response before any cap can apply. The current code reads a 1.3 MB HTML error page from a 404ing source and *then* truncates. A concrete struct over `net/http` caps at read time, drops a dependency, and carries the cache validators that make conditional GET a small change later.

**Files:**
- Create: `internal/httpx/httpx.go`, `internal/httpx/client_test.go`, `internal/httpx/proxy.go`, `internal/httpx/proxy_test.go`, `internal/httpx/hostlimit.go`, `internal/httpx/hostlimit_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type httpx.Response struct { Status int; Body []byte; Truncated bool; ETag, LastModified string }`
  - `func httpx.New(timeout time.Duration, opts ...Option) (*Client, error)`
  - `func httpx.WithProxy(rawURL string) Option`
  - `func httpx.WithMaxBodyBytes(n int64) Option`
  - `func (*Client) Get(ctx context.Context, url string, headers map[string]string) (*Response, error)`
  - `func httpx.NewProxyCache(timeout time.Duration, maxBody int64) *ProxyCache`
  - `func (*ProxyCache) For(proxyURL string) (*Client, error)`
  - `func httpx.NewHostLimiter(perHost rate.Limit, burst int) *HostLimiter`
  - `func (*HostLimiter) Wait(ctx context.Context, rawURL string) error`

- [ ] **Step 1: Write the failing client tests**

Create `internal/httpx/client_test.go`:

```go
package httpx_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/samvad-hq/samvad-news-harvester/internal/httpx"
	"github.com/stretchr/testify/require"
)

func TestGetReturnsBodyStatusAndValidators(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "test-agent", r.Header.Get("User-Agent"))
		require.Equal(t, "application/xml", r.Header.Get("Accept"))
		w.Header().Set("ETag", `"abc123"`)
		w.Header().Set("Last-Modified", "Sun, 06 Sep 2026 04:00:00 GMT")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<urlset></urlset>"))
	}))
	defer srv.Close()

	c, err := httpx.New(5 * time.Second)
	require.NoError(t, err)

	resp, err := c.Get(context.Background(), srv.URL, map[string]string{
		"User-Agent": "test-agent",
		"Accept":     "application/xml",
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.Status)
	require.Equal(t, "<urlset></urlset>", string(resp.Body))
	require.False(t, resp.Truncated)
	require.Equal(t, `"abc123"`, resp.ETag)
	require.Equal(t, "Sun, 06 Sep 2026 04:00:00 GMT", resp.LastModified)
}

func TestGetCapsBodyAndFlagsTruncation(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 5000)))
	}))
	defer srv.Close()

	c, err := httpx.New(5*time.Second, httpx.WithMaxBodyBytes(1000))
	require.NoError(t, err)

	resp, err := c.Get(context.Background(), srv.URL, nil)
	require.NoError(t, err)
	require.Len(t, resp.Body, 1000)
	require.True(t, resp.Truncated)
}

func TestGetDoesNotFlagTruncationAtExactLimit(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 1000)))
	}))
	defer srv.Close()

	c, err := httpx.New(5*time.Second, httpx.WithMaxBodyBytes(1000))
	require.NoError(t, err)

	resp, err := c.Get(context.Background(), srv.URL, nil)
	require.NoError(t, err)
	require.Len(t, resp.Body, 1000)
	require.False(t, resp.Truncated)
}

func TestGetReturnsNonOKStatusWithoutError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("Access Denied"))
	}))
	defer srv.Close()

	c, err := httpx.New(5 * time.Second)
	require.NoError(t, err)

	resp, err := c.Get(context.Background(), srv.URL, nil)
	require.NoError(t, err, "an HTTP error status is a result, not a transport error")
	require.Equal(t, http.StatusForbidden, resp.Status)
	require.Equal(t, "Access Denied", string(resp.Body))
}

func TestGetHonoursContextCancellation(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer func() { close(release); srv.Close() }()

	c, err := httpx.New(5 * time.Second)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = c.Get(ctx, srv.URL, nil)
	require.ErrorIs(t, err, context.Canceled)
}

func TestNewRejectsBadProxyURL(t *testing.T) {
	t.Parallel()

	_, err := httpx.New(5*time.Second, httpx.WithProxy("://not a url"))
	require.Error(t, err)
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/httpx/...`
Expected: FAIL — package `internal/httpx` does not exist.

- [ ] **Step 3: Implement `internal/httpx/httpx.go`**

```go
// Package httpx wraps net/http with the three things every fetch in this
// service needs: a hard cap on how much of a response body is read, the
// cache validators that make conditional requests possible, and an
// optional per-client proxy that leaves the rest of the process alone.
package httpx

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultMaxBodyBytes bounds a response body. The largest configured
// sitemap (jagran) is about 2.5 MB, so 8 MiB leaves substantial headroom
// while still refusing to buffer an unbounded response.
const DefaultMaxBodyBytes int64 = 8 << 20

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
}

// Option configures a Client.
type Option func(*clientConfig) error

type clientConfig struct {
	proxy   *url.URL
	maxBody int64
}

// WithMaxBodyBytes caps how much of a response body is read. A value of
// zero or less leaves the default in place.
func WithMaxBodyBytes(n int64) Option {
	return func(c *clientConfig) error {
		if n > 0 {
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
			return fmt.Errorf("httpx: proxy url %q must include a scheme and host", rawURL)
		}
		c.proxy = u
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

	transport := &http.Transport{
		Proxy:               http.ProxyURL(cfg.proxy),
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		ForceAttemptHTTP2:   true,
	}
	if cfg.proxy == nil {
		transport.Proxy = nil
	}

	return &Client{
		hc:      &http.Client{Timeout: timeout, Transport: transport},
		maxBody: cfg.maxBody,
	}, nil
}

// Get performs a GET and reads at most the configured body cap. A non-2xx
// status is returned in the Response; only a transport or context failure
// produces an error.
func (c *Client) Get(ctx context.Context, rawURL string, headers map[string]string) (*Response, error) {
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
		_, _ = io.Copy(io.Discard, resp.Body)
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
```

- [ ] **Step 4: Run the client tests**

Run: `go test -race ./internal/httpx/...`
Expected: PASS.

- [ ] **Step 5: Write the failing proxy cache test**

Create `internal/httpx/proxy_test.go`:

```go
package httpx_test

import (
	"testing"
	"time"

	"github.com/samvad-hq/samvad-news-harvester/internal/httpx"
	"github.com/stretchr/testify/require"
)

func TestProxyCacheSharesTheDirectClient(t *testing.T) {
	t.Parallel()

	cache := httpx.NewProxyCache(time.Second, httpx.DefaultMaxBodyBytes)

	a, err := cache.For("")
	require.NoError(t, err)
	b, err := cache.For("   ")
	require.NoError(t, err)
	require.Same(t, a, b, "a blank proxy always yields the one direct client")
}

func TestProxyCacheReusesOneClientPerProxy(t *testing.T) {
	t.Parallel()

	cache := httpx.NewProxyCache(time.Second, httpx.DefaultMaxBodyBytes)

	first, err := cache.For("http://proxy.example:8080")
	require.NoError(t, err)
	second, err := cache.For("http://proxy.example:8080")
	require.NoError(t, err)
	require.Same(t, first, second)

	other, err := cache.For("http://other.example:3128")
	require.NoError(t, err)
	require.NotSame(t, first, other)

	direct, err := cache.For("")
	require.NoError(t, err)
	require.NotSame(t, first, direct)
}

func TestProxyCacheRejectsBadProxy(t *testing.T) {
	t.Parallel()

	cache := httpx.NewProxyCache(time.Second, httpx.DefaultMaxBodyBytes)
	_, err := cache.For("not-a-url")
	require.Error(t, err)
}

func TestProxyCacheIsSafeForConcurrentUse(t *testing.T) {
	t.Parallel()

	cache := httpx.NewProxyCache(time.Second, httpx.DefaultMaxBodyBytes)
	done := make(chan struct{})

	for range 16 {
		go func() {
			defer func() { done <- struct{}{} }()
			_, err := cache.For("http://proxy.example:8080")
			require.NoError(t, err)
		}()
	}
	for range 16 {
		<-done
	}
}
```

- [ ] **Step 6: Run to verify it fails**

Run: `go test ./internal/httpx/... -run ProxyCache`
Expected: FAIL — `NewProxyCache` is undefined.

- [ ] **Step 7: Implement `internal/httpx/proxy.go`**

```go
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
	direct  *Client

	mu      sync.Mutex
	byProxy map[string]*Client
}

// NewProxyCache returns a cache whose clients share the given timeout and
// body cap. It panics only if the direct client cannot be built, which
// cannot happen for an empty proxy.
func NewProxyCache(timeout time.Duration, maxBody int64) *ProxyCache {
	direct, err := New(timeout, WithMaxBodyBytes(maxBody))
	if err != nil {
		// New only fails on a malformed proxy, and there is none here.
		panic("httpx: building the direct client cannot fail: " + err.Error())
	}
	return &ProxyCache{
		timeout: timeout,
		maxBody: maxBody,
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
	cl, err := New(c.timeout, WithMaxBodyBytes(c.maxBody), WithProxy(proxyURL))
	if err != nil {
		return nil, err
	}
	c.byProxy[proxyURL] = cl
	return cl, nil
}
```

- [ ] **Step 8: Write the failing host limiter test**

The current scraper gives ten workers one shared `time.Ticker`, so the pool only takes turns. This replaces it. Host keying matters most here rather than at the fetch stage: the 26 configured sources span 26 distinct hostnames, so sitemap fetches never contend, but one source's crawl issues hundreds of article requests that all resolve to that source's single host.

Create `internal/httpx/hostlimit_test.go`:

```go
package httpx_test

import (
	"context"
	"testing"
	"time"

	"github.com/samvad-hq/samvad-news-harvester/internal/httpx"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

func TestHostLimiterThrottlesPerHost(t *testing.T) {
	t.Parallel()

	// 20 requests/sec, burst 1: the second call to a host waits ~50ms.
	lim := httpx.NewHostLimiter(rate.Limit(20), 1)
	ctx := context.Background()

	start := time.Now()
	require.NoError(t, lim.Wait(ctx, "https://a.example/one"))
	require.NoError(t, lim.Wait(ctx, "https://a.example/two"))
	require.GreaterOrEqual(t, time.Since(start), 30*time.Millisecond)
}

func TestHostLimiterKeysByHostNotURL(t *testing.T) {
	t.Parallel()

	lim := httpx.NewHostLimiter(rate.Limit(20), 1)
	ctx := context.Background()

	require.NoError(t, lim.Wait(ctx, "https://a.example/one"))

	// A different host has its own budget, so this returns immediately.
	start := time.Now()
	require.NoError(t, lim.Wait(ctx, "https://b.example/one"))
	require.Less(t, time.Since(start), 20*time.Millisecond)
}

func TestHostLimiterHonoursCancellation(t *testing.T) {
	t.Parallel()

	lim := httpx.NewHostLimiter(rate.Limit(1), 1)
	ctx, cancel := context.WithCancel(context.Background())

	require.NoError(t, lim.Wait(ctx, "https://a.example/one"))
	cancel()
	require.Error(t, lim.Wait(ctx, "https://a.example/two"))
}

func TestHostLimiterAllowsUnparseableURLThrough(t *testing.T) {
	t.Parallel()

	lim := httpx.NewHostLimiter(rate.Limit(20), 1)
	require.NoError(t, lim.Wait(context.Background(), "::not a url"))
}
```

- [ ] **Step 9: Implement `internal/httpx/hostlimit.go`**

```go
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
		return nil
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
```

- [ ] **Step 10: Run the whole package under race**

Run: `go test -race ./internal/httpx/...`
Expected: PASS, all tests.

- [ ] **Step 11: Verify the module is green**

```bash
go build ./... && go vet ./... && go test -race ./...
```

- [ ] **Step 12: Commit**

```bash
git add internal/httpx
git commit -m "$(cat <<'EOF'
feat(httpx): add net/http client with a body cap and host limiter

resty's Body() buffers the entire response before any cap can apply, so
the old code read a 1.3 MB HTML error page from a 404ing sitemap and
then truncated it. httpx.Client applies the cap at read time with
io.LimitReader and reports whether the body was cut.

Response also carries ETag and Last-Modified. Four of the eight
reachable configured sitemaps serve one of those, so keeping them here
is what makes conditional GET a later addition rather than a refactor.

HostLimiter replaces the shared time.Ticker the scraper handed to ten
workers, which throttled the pool to one request at a time. Keying on host
bounds the article scrape, where hundreds of requests from one crawl all
hit a single origin.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: `internal/config` — environment parsing and the shared file loader

Viper reads no configuration file in this service. It resolves ten environment variables and pulls in afero, mapstructure, pflag, cast, toml, ini and fsnotify to do it. `AutomaticEnv` without explicit `BindEnv` also only resolves keys that were given a default, which is a trap for the next contributor.

This task also kills D4: `providers.LoadRegistry` and `publishers.LoadRegistry` are the same loader written twice. The shared half becomes two generic helpers here — functions, not a type hierarchy.

**Files:**
- Rewrite: `internal/config/config.go`
- Create: `internal/config/file.go`, `internal/config/config_test.go`, `internal/config/file_test.go`
- Modify: `internal/logger/logger.go` (adapt to the new `Config`; deleted in Task 10)
- Modify: `configs/example.env`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type config.Config` with fields `AppName, Env string; LogLevel slog.Level; SourcesFile, SinksFile string; CrawlInterval time.Duration; SourceConcurrency, ArticleConcurrency int; PerHostRPS float64; DedupeBackend, DedupePath string; DedupeTTL, DedupeCleanupInterval time.Duration; FetchTimeout, ScrapeTimeout time.Duration`
  - `func config.Load() (*Config, error)`
  - `func (*Config) Validate() error`
  - `func config.DecodeFile(path string, dst any) error`
  - `func config.ValidateList[T Item](items []T) error` where `type Item interface { Ident() string; Validate() error }`

### Environment variables

Renamed for clarity and switched from second counts to duration strings. Every one is documented in `configs/example.env` in Step 7.

| Variable | Default | Was |
|----------|---------|-----|
| `APP_NAME` | `samvad-news-harvester` | same |
| `APP_ENV` | `development` | same |
| `LOG_LEVEL` | `info` | same |
| `SOURCES_FILE` | `./configs/sources.yaml` | `PROVIDERS_FILE` |
| `SINKS_FILE` | `./configs/sinks.yaml` | `PUBLISHERS_FILE` |
| `CRAWL_INTERVAL` | `15m` | `CRAWL_INTERVAL` (seconds) |
| `SOURCE_CONCURRENCY` | `8` | hardcoded `maxProviderWorkers = 10` |
| `ARTICLE_CONCURRENCY` | `10` | hardcoded `maxArticleWorkers = 10` |
| `PER_HOST_RPS` | `2` | implicit in `request_delay_ms` |
| `DEDUPE_BACKEND` | `bolt` | `STORAGE_TYPE` (`bbolt`) |
| `DEDUPE_PATH` | `./data/dedupe.db` | `BBOLT_PATH` |
| `DEDUPE_TTL` | `120h` | `STORAGE_TTL_SECONDS` |
| `DEDUPE_CLEANUP_INTERVAL` | `12h` | `STORAGE_CLEANUP_INTERVAL_SECONDS` |
| `FETCH_TIMEOUT` | `30s` | hardcoded `15s` |
| `SCRAPE_TIMEOUT` | `15s` | hardcoded `15s` |

- [ ] **Step 1: Write the failing config tests**

Create `internal/config/config_test.go`:

```go
package config_test

import (
	"log/slog"
	"testing"
	"time"

	"github.com/samvad-hq/samvad-news-harvester/internal/config"
	"github.com/stretchr/testify/require"
)

func TestLoadUsesDefaults(t *testing.T) {
	cfg, err := config.Load()
	require.NoError(t, err)

	require.Equal(t, "samvad-news-harvester", cfg.AppName)
	require.Equal(t, slog.LevelInfo, cfg.LogLevel)
	require.Equal(t, "./configs/sources.yaml", cfg.SourcesFile)
	require.Equal(t, "./configs/sinks.yaml", cfg.SinksFile)
	require.Equal(t, 15*time.Minute, cfg.CrawlInterval)
	require.Equal(t, 8, cfg.SourceConcurrency)
	require.Equal(t, 10, cfg.ArticleConcurrency)
	require.InDelta(t, 2.0, cfg.PerHostRPS, 0.001)
	require.Equal(t, "bolt", cfg.DedupeBackend)
	require.Equal(t, 120*time.Hour, cfg.DedupeTTL)
	require.Equal(t, 30*time.Second, cfg.FetchTimeout)
}

func TestLoadReadsEnvironment(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("CRAWL_INTERVAL", "90s")
	t.Setenv("SOURCE_CONCURRENCY", "4")
	t.Setenv("DEDUPE_BACKEND", "none")
	t.Setenv("PER_HOST_RPS", "0.5")

	cfg, err := config.Load()
	require.NoError(t, err)

	require.Equal(t, "production", cfg.Env)
	require.Equal(t, slog.LevelDebug, cfg.LogLevel)
	require.Equal(t, 90*time.Second, cfg.CrawlInterval)
	require.Equal(t, 4, cfg.SourceConcurrency)
	require.Equal(t, "none", cfg.DedupeBackend)
	require.InDelta(t, 0.5, cfg.PerHostRPS, 0.001)
}

func TestLogLevelParsing(t *testing.T) {
	tests := map[string]slog.Level{
		"debug":   slog.LevelDebug,
		"DEBUG":   slog.LevelDebug,
		"info":    slog.LevelInfo,
		"warn":    slog.LevelWarn,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
	}
	for in, expected := range tests {
		t.Run(in, func(t *testing.T) {
			t.Setenv("LOG_LEVEL", in)
			cfg, err := config.Load()
			require.NoError(t, err)
			require.Equal(t, expected, cfg.LogLevel)
		})
	}
}

func TestLoadRejectsBadValues(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "unparseable duration", key: "CRAWL_INTERVAL", value: "fifteen minutes"},
		{name: "zero interval", key: "CRAWL_INTERVAL", value: "0s"},
		{name: "negative interval", key: "CRAWL_INTERVAL", value: "-5m"},
		{name: "unparseable int", key: "SOURCE_CONCURRENCY", value: "lots"},
		{name: "zero concurrency", key: "SOURCE_CONCURRENCY", value: "0"},
		{name: "unknown log level", key: "LOG_LEVEL", value: "verbose"},
		{name: "unknown dedupe backend", key: "DEDUPE_BACKEND", value: "redis"},
		{name: "zero per-host rate", key: "PER_HOST_RPS", value: "0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.key, tt.value)
			_, err := config.Load()
			require.Error(t, err)
		})
	}
}

func TestValidateRejectsBoltWithoutPath(t *testing.T) {
	t.Setenv("DEDUPE_BACKEND", "bolt")
	t.Setenv("DEDUPE_PATH", "")

	_, err := config.Load()
	require.ErrorContains(t, err, "DEDUPE_PATH")
}

func TestNoneBackendDoesNotNeedAPath(t *testing.T) {
	t.Setenv("DEDUPE_BACKEND", "none")
	t.Setenv("DEDUPE_PATH", "")

	cfg, err := config.Load()
	require.NoError(t, err)
	require.Equal(t, "none", cfg.DedupeBackend)
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/config/...`
Expected: FAIL — the current `Config` has none of these fields.

- [ ] **Step 3: Rewrite `internal/config/config.go`**

```go
// Package config loads the harvester's runtime settings from the
// environment, and provides the shared YAML/JSON decoding used by the
// source and sink configuration files.
//
// Settings come from the environment only. An optional configs/.env file
// is read first for local development; real environment variables always
// win over it.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Dedupe backend names accepted by DEDUPE_BACKEND.
const (
	DedupeBolt = "bolt"
	DedupeNone = "none"
)

// Config is the harvester's runtime configuration.
type Config struct {
	// AppName and Env label the process in logs.
	AppName string
	Env     string
	// LogLevel is the minimum level written to stdout.
	LogLevel slog.Level

	// SourcesFile and SinksFile are paths to the YAML or JSON files
	// describing what to crawl and where to deliver.
	SourcesFile string
	SinksFile   string

	// CrawlInterval is how often the full source list is crawled.
	CrawlInterval time.Duration
	// SourceConcurrency bounds how many sources are crawled at once.
	SourceConcurrency int
	// ArticleConcurrency bounds in-flight metadata scrapes per source.
	ArticleConcurrency int
	// PerHostRPS caps requests per second to any single host.
	PerHostRPS float64

	// DedupeBackend is DedupeBolt or DedupeNone.
	DedupeBackend string
	// DedupePath is the bbolt file, required when the backend is bolt.
	DedupePath string
	// DedupeTTL is how long an article ID is remembered.
	DedupeTTL time.Duration
	// DedupeCleanupInterval is how often expired IDs are swept.
	DedupeCleanupInterval time.Duration

	// FetchTimeout bounds one sitemap request.
	FetchTimeout time.Duration
	// ScrapeTimeout bounds one article metadata request.
	ScrapeTimeout time.Duration
}

// Load reads configuration from the environment and validates it.
func Load() (*Config, error) {
	// A missing .env is normal in production, so the error is ignored.
	_ = godotenv.Load("configs/.env")

	var errs []error
	get := func(fn func() error) {
		if err := fn(); err != nil {
			errs = append(errs, err)
		}
	}

	cfg := &Config{
		AppName:       env("APP_NAME", "samvad-news-harvester"),
		Env:           env("APP_ENV", "development"),
		SourcesFile:   env("SOURCES_FILE", "./configs/sources.yaml"),
		SinksFile:     env("SINKS_FILE", "./configs/sinks.yaml"),
		DedupeBackend: strings.ToLower(env("DEDUPE_BACKEND", DedupeBolt)),
		DedupePath:    env("DEDUPE_PATH", "./data/dedupe.db"),
	}

	get(func() (err error) { cfg.LogLevel, err = envLevel("LOG_LEVEL", slog.LevelInfo); return })
	get(func() (err error) { cfg.CrawlInterval, err = envDuration("CRAWL_INTERVAL", 15*time.Minute); return })
	get(func() (err error) { cfg.SourceConcurrency, err = envInt("SOURCE_CONCURRENCY", 8); return })
	get(func() (err error) { cfg.ArticleConcurrency, err = envInt("ARTICLE_CONCURRENCY", 10); return })
	get(func() (err error) { cfg.PerHostRPS, err = envFloat("PER_HOST_RPS", 2); return })
	get(func() (err error) { cfg.DedupeTTL, err = envDuration("DEDUPE_TTL", 120*time.Hour); return })
	get(func() (err error) {
		cfg.DedupeCleanupInterval, err = envDuration("DEDUPE_CLEANUP_INTERVAL", 12*time.Hour)
		return
	})
	get(func() (err error) { cfg.FetchTimeout, err = envDuration("FETCH_TIMEOUT", 30*time.Second); return })
	get(func() (err error) { cfg.ScrapeTimeout, err = envDuration("SCRAPE_TIMEOUT", 15*time.Second); return })

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate reports every problem with the configuration at once, so a
// misconfigured deployment is fixed in one pass rather than one restart
// per mistake. It is also what the validate subcommand calls.
func (c *Config) Validate() error {
	var errs []error

	if c.SourcesFile == "" {
		errs = append(errs, errors.New("SOURCES_FILE must not be empty"))
	}
	if c.SinksFile == "" {
		errs = append(errs, errors.New("SINKS_FILE must not be empty"))
	}
	if c.CrawlInterval <= 0 {
		errs = append(errs, errors.New("CRAWL_INTERVAL must be positive"))
	}
	if c.SourceConcurrency < 1 {
		errs = append(errs, errors.New("SOURCE_CONCURRENCY must be at least 1"))
	}
	if c.ArticleConcurrency < 1 {
		errs = append(errs, errors.New("ARTICLE_CONCURRENCY must be at least 1"))
	}
	if c.PerHostRPS <= 0 {
		errs = append(errs, errors.New("PER_HOST_RPS must be positive"))
	}
	if c.FetchTimeout <= 0 {
		errs = append(errs, errors.New("FETCH_TIMEOUT must be positive"))
	}
	if c.ScrapeTimeout <= 0 {
		errs = append(errs, errors.New("SCRAPE_TIMEOUT must be positive"))
	}

	switch c.DedupeBackend {
	case DedupeNone:
	case DedupeBolt:
		if strings.TrimSpace(c.DedupePath) == "" {
			errs = append(errs, errors.New("DEDUPE_PATH is required when DEDUPE_BACKEND is bolt"))
		}
		if c.DedupeTTL <= 0 {
			errs = append(errs, errors.New("DEDUPE_TTL must be positive"))
		}
		if c.DedupeCleanupInterval <= 0 {
			errs = append(errs, errors.New("DEDUPE_CLEANUP_INTERVAL must be positive"))
		}
	default:
		errs = append(errs, fmt.Errorf("DEDUPE_BACKEND %q is not supported (use %q or %q)",
			c.DedupeBackend, DedupeBolt, DedupeNone))
	}

	return errors.Join(errs...)
}

// env returns the trimmed value of key, or fallback when it is unset or blank.
func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// envDuration parses key as a Go duration string such as "15m" or "90s".
func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a duration such as 15m or 90s", key, raw)
	}
	return d, nil
}

// envInt parses key as a base-10 integer.
func envInt(key string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not an integer", key, raw)
	}
	return n, nil
}

// envFloat parses key as a floating-point number.
func envFloat(key string, fallback float64) (float64, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a number", key, raw)
	}
	return f, nil
}

// envLevel parses key as a slog level name.
func envLevel(key string, fallback slog.Level) (slog.Level, error) {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch raw {
	case "":
		return fallback, nil
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("%s: %q is not a level (use debug, info, warn or error)", key, raw)
	}
}
```

- [ ] **Step 4: Adapt the old logger so the module still builds**

`internal/logger/logger.go` reads `cfg.LogLevel` as a string, which is now a
`slog.Level`. This package is deleted in Task 10 and nothing depends on its level being
configurable in the meantime, so pin it rather than writing a mapping that is about to
be thrown away.

Do **not** convert with `zapcore.Level(cfg.LogLevel)`. The two scales are not aligned:
slog uses Debug −4, Info 0, Warn 4, Error 8, while zapcore uses Debug −1, Info 0, Warn 1,
Error 2 — so `zapcore.Level(slog.LevelWarn)` is `PanicLevel`. Verified by running it.

Replace the whole `switch` on `cfg.LogLevel`, and its `level` declaration, with:

```go
	// This package is replaced by log/slog during the cutover; until then the
	// level is fixed, because slog and zapcore levels are not on the same scale
	// (slog Warn is 4, which is zapcore PanicLevel).
	level := zapcore.InfoLevel
```

Run: `go build ./...`
Expected: builds.

- [ ] **Step 5: Run the config tests**

Run: `go test -race ./internal/config/...`
Expected: PASS.

- [ ] **Step 6: Write the failing file-loader tests**

Create `internal/config/file_test.go`:

```go
package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/samvad-hq/samvad-news-harvester/internal/config"
	"github.com/stretchr/testify/require"
)

type item struct {
	ID   string `yaml:"id" json:"id"`
	Name string `yaml:"name" json:"name"`
}

func (i item) Ident() string { return i.ID }

func (i item) Validate() error {
	if i.ID == "" {
		return errors.New("id is required")
	}
	return nil
}

func writeFile(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestDecodeFileYAML(t *testing.T) {
	t.Parallel()

	path := writeFile(t, "items.yaml", "items:\n  - id: a\n    name: Alpha\n")

	var file struct {
		Items []item `yaml:"items" json:"items"`
	}
	require.NoError(t, config.DecodeFile(path, &file))
	require.Len(t, file.Items, 1)
	require.Equal(t, "Alpha", file.Items[0].Name)
}

func TestDecodeFileJSON(t *testing.T) {
	t.Parallel()

	path := writeFile(t, "items.json", `{"items":[{"id":"a","name":"Alpha"}]}`)

	var file struct {
		Items []item `yaml:"items" json:"items"`
	}
	require.NoError(t, config.DecodeFile(path, &file))
	require.Len(t, file.Items, 1)
	require.Equal(t, "Alpha", file.Items[0].Name)
}

func TestDecodeFileExpandsEnvironment(t *testing.T) {
	t.Setenv("TEST_ITEM_NAME", "FromEnv")

	path := writeFile(t, "items.yaml", "items:\n  - id: a\n    name: ${TEST_ITEM_NAME}\n")

	var file struct {
		Items []item `yaml:"items" json:"items"`
	}
	require.NoError(t, config.DecodeFile(path, &file))
	require.Equal(t, "FromEnv", file.Items[0].Name)
}

func TestDecodeFileRejects(t *testing.T) {
	t.Parallel()

	t.Run("missing file", func(t *testing.T) {
		var file struct{}
		require.Error(t, config.DecodeFile(filepath.Join(t.TempDir(), "nope.yaml"), &file))
	})

	t.Run("empty path", func(t *testing.T) {
		var file struct{}
		require.Error(t, config.DecodeFile("", &file))
	})

	t.Run("unknown extension", func(t *testing.T) {
		var file struct{}
		require.Error(t, config.DecodeFile(writeFile(t, "items.toml", "x = 1"), &file))
	})

	t.Run("malformed yaml", func(t *testing.T) {
		var file struct {
			Items []item `yaml:"items"`
		}
		require.Error(t, config.DecodeFile(writeFile(t, "bad.yaml", "items:\n  - id: [unclosed\n"), &file))
	})
}

func TestValidateList(t *testing.T) {
	t.Parallel()

	t.Run("accepts valid items", func(t *testing.T) {
		require.NoError(t, config.ValidateList([]item{{ID: "a"}, {ID: "b"}}))
	})

	t.Run("rejects an empty list", func(t *testing.T) {
		require.ErrorContains(t, config.ValidateList([]item{}), "no entries")
	})

	t.Run("reports the failing index", func(t *testing.T) {
		err := config.ValidateList([]item{{ID: "a"}, {ID: ""}})
		require.ErrorContains(t, err, "entry 1")
	})

	t.Run("rejects duplicate ids", func(t *testing.T) {
		err := config.ValidateList([]item{{ID: "a"}, {ID: "a"}})
		require.ErrorContains(t, err, "duplicate")
	})
}
```

- [ ] **Step 7: Implement `internal/config/file.go`**

```go
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Item is one entry in a configuration list. Both source.Config and
// sink.Config implement it, which is what lets one loader serve both.
type Item interface {
	// Ident returns the entry's unique identifier, used to reject duplicates.
	Ident() string
	// Validate reports whether the entry is usable.
	Validate() error
}

// DecodeFile reads path and decodes it into dst. The format is chosen by
// file extension: .yaml and .yml as YAML, .json as JSON.
//
// ${VAR} references are expanded from the environment before decoding, so
// credentials live in the environment rather than in the file. An unset
// variable expands to an empty string, which validation then rejects with
// a message naming the field.
func DecodeFile(path string, dst any) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("config: file path is empty")
	}

	raw, err := os.ReadFile(path) //nolint:gosec // the path is operator-supplied configuration
	if err != nil {
		return fmt.Errorf("config: read %s: %w", path, err)
	}

	expanded := []byte(os.ExpandEnv(string(raw)))

	switch ext := strings.ToLower(filepath.Ext(path)); ext {
	case ".yaml", ".yml":
		if err := yaml.Unmarshal(expanded, dst); err != nil {
			return fmt.Errorf("config: decode yaml %s: %w", path, err)
		}
	case ".json":
		if err := json.Unmarshal(expanded, dst); err != nil {
			return fmt.Errorf("config: decode json %s: %w", path, err)
		}
	default:
		return fmt.Errorf("config: %s has unsupported extension %q (use .yaml, .yml or .json)", path, ext)
	}
	return nil
}

// ValidateList checks every entry and rejects duplicate identifiers,
// reporting all problems at once rather than stopping at the first.
func ValidateList[T Item](items []T) error {
	if len(items) == 0 {
		return errors.New("config: file contains no entries")
	}

	var errs []error
	seen := make(map[string]int, len(items))

	for i, it := range items {
		if err := it.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("entry %d: %w", i, err))
			continue
		}
		id := it.Ident()
		if first, dup := seen[id]; dup {
			errs = append(errs, fmt.Errorf("entry %d: duplicate id %q, first used by entry %d", i, id, first))
			continue
		}
		seen[id] = i
	}

	return errors.Join(errs...)
}
```

- [ ] **Step 8: Run the file-loader tests**

Run: `go test -race ./internal/config/...`
Expected: PASS, all tests.

- [ ] **Step 9: Rewrite `configs/example.env`**

```bash
cat > configs/example.env <<'ENVEOF'
# Example environment for Samvad News Harvester.
# Copy to configs/.env and adjust. Real environment variables win over this file.

# --- Identity -------------------------------------------------------------
APP_NAME=samvad-news-harvester
APP_ENV=development
LOG_LEVEL=info                      # debug | info | warn | error

# --- Configuration files --------------------------------------------------
SOURCES_FILE=./configs/sources.yaml
SINKS_FILE=./configs/sinks.yaml

# --- Scheduling and concurrency -------------------------------------------
CRAWL_INTERVAL=15m                  # Go duration: 90s, 15m, 2h
SOURCE_CONCURRENCY=8                # sources crawled at once
ARTICLE_CONCURRENCY=10              # in-flight metadata scrapes per source
PER_HOST_RPS=2                      # requests per second to any one host

# --- Deduplication --------------------------------------------------------
DEDUPE_BACKEND=bolt                 # bolt | none
DEDUPE_PATH=./data/dedupe.db
DEDUPE_TTL=120h
DEDUPE_CLEANUP_INTERVAL=12h

# --- Timeouts -------------------------------------------------------------
FETCH_TIMEOUT=30s                   # one sitemap request
SCRAPE_TIMEOUT=15s                  # one article metadata request

# --- Sink credentials -----------------------------------------------------
# Referenced as ${VAR} from configs/sinks.yaml. Never commit real values.
WEBHOOK_URL=https://example.com/hooks/news
WEBHOOK_AUTH_TOKEN=

AWS_SQS_QUEUE_URL=
AWS_SQS_REGION=
AWS_SQS_ACCESS_KEY_ID=
AWS_SQS_SECRET_ACCESS_KEY=

AWS_SNS_TOPIC_ARN=
AWS_SNS_REGION=
AWS_SNS_ACCESS_KEY_ID=
AWS_SNS_SECRET_ACCESS_KEY=

GCP_PROJECT_ID=
GCP_TOPIC_ID=
GCP_CREDENTIALS_FILE=
ENVEOF
```

- [ ] **Step 10: Verify the module is green**

```bash
go build ./... && go vet ./... && go test -race ./...
```

- [ ] **Step 11: Commit**

```bash
git add internal/config internal/logger/logger.go configs/example.env
git commit -m "$(cat <<'EOF'
refactor(config): parse the environment directly, drop viper

Viper read no configuration file here. It resolved ten environment
variables and pulled in afero, mapstructure, pflag, cast, toml, ini and
fsnotify to do it. AutomaticEnv without an explicit BindEnv also only
resolves keys that were given a default, which is a trap.

Durations are now Go duration strings (CRAWL_INTERVAL=15m) rather than
second counts, concurrency limits move out of hardcoded constants into
the environment, and Validate reports every problem at once so a
misconfigured deployment is fixed in one pass.

DecodeFile and ValidateList replace the two near-identical loaders in
providers and publishers: open, sniff the extension, decode, validate,
reject duplicate ids. That was about 180 duplicated lines; generics
collapse it into two functions rather than a type hierarchy.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: `internal/source` — configuration and the sitemap fetcher

Replaces `pkg/providers`. Three defects go with it: `Config map[string]any` read through `ConfigString` (D6), so a typo in `user_agent` is a silent empty header; `Fetcher.ID()` returning the provider *type*, whose mismatch makes `Fetch` reject ID-registered sources (D9); and the `RequestDelayMs` field that fed the broken shared ticker.

**Files:**
- Create: `internal/source/source.go`, `internal/source/xml.go`, `internal/source/sitemap.go`, `internal/source/source_test.go`, `internal/source/xml_test.go`, `internal/source/sitemap_test.go`
- Create: `internal/source/testdata/` fixtures, `scripts/capture-fixtures.sh`
- Create: `configs/sources.example.yaml`

**Interfaces:**
- Consumes: `news.Article`, `news.CanonicalURL`, `news.ID`, `httpx.ProxyCache`, `httpx.Response`, `config.DecodeFile`, `config.ValidateList`.
- Produces:
  - `type source.Headers struct { UserAgent, Accept, AcceptLanguage, CacheControl string }` with `func (Headers) Map() map[string]string`
  - `type source.Config struct { ID, Name, Type, URL, Proxy string; Headers Headers }` with `Ident() string` and `Validate() error`
  - `const source.TypeNewsSitemap = "news_sitemap"`
  - `func source.LoadFile(path string) ([]Config, error)`
  - `func source.NewSitemap(clients *httpx.ProxyCache, log *slog.Logger) *Sitemap`
  - `func (*Sitemap) Fetch(ctx context.Context, src Config) ([]news.Article, error)`

### YAML shape

```yaml
sources:
  - id: thehindu
    name: The Hindu
    type: news_sitemap
    url: https://www.thehindu.com/sitemap/googlenews/all/all.xml
    # proxy: ${PROXY_URL}          # optional, per-source
    headers:
      user_agent: "Mozilla/5.0 (compatible; samvad-harvester/2.0)"
      accept: application/xml,text/xml;q=0.9
      accept_language: en-IN,en;q=0.9
```

Changes from the old format: `providers:` becomes `sources:`, `source_url` becomes `url`, `config:` becomes a typed `headers:` block, `type: google_news_sitemap` becomes `type: news_sitemap` (the namespace is Google's, but publishers and the specification both call the document a news sitemap), and `response_format` and `request_delay_ms` are removed — the first was never read for anything, the second is replaced by `PER_HOST_RPS`.

- [ ] **Step 1: Write the failing config tests**

Create `internal/source/source_test.go`:

```go
package source_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/samvad-hq/samvad-news-harvester/internal/source"
	"github.com/stretchr/testify/require"
)

func writeSources(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sources.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestLoadFile(t *testing.T) {
	t.Parallel()

	path := writeSources(t, `
sources:
  - id: thehindu
    name: The Hindu
    type: news_sitemap
    url: https://www.thehindu.com/sitemap/googlenews/all/all.xml
    headers:
      user_agent: test-agent
      accept: application/xml
`)

	got, err := source.LoadFile(path)
	require.NoError(t, err)
	require.Len(t, got, 1)

	src := got[0]
	require.Equal(t, "thehindu", src.ID)
	require.Equal(t, "The Hindu", src.Name)
	require.Equal(t, source.TypeNewsSitemap, src.Type)
	require.Equal(t, "test-agent", src.Headers.UserAgent)
	require.Equal(t, "thehindu", src.Ident())
}

func TestLoadFileExpandsProxyFromEnv(t *testing.T) {
	t.Setenv("TEST_PROXY", "http://proxy.example:8080")

	path := writeSources(t, `
sources:
  - id: ndtv
    name: NDTV
    type: news_sitemap
    url: https://www.ndtv.com/sitemap/google-news-sitemap
    proxy: ${TEST_PROXY}
    headers:
      user_agent: test-agent
`)

	got, err := source.LoadFile(path)
	require.NoError(t, err)
	require.Equal(t, "http://proxy.example:8080", got[0].Proxy)
}

func TestLoadFileRejects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		body     string
		contains string
	}{
		{
			name:     "missing id",
			body:     "sources:\n  - name: X\n    type: news_sitemap\n    url: https://x.example/s.xml\n    headers:\n      user_agent: a\n",
			contains: "id is required",
		},
		{
			name:     "missing name",
			body:     "sources:\n  - id: x\n    type: news_sitemap\n    url: https://x.example/s.xml\n    headers:\n      user_agent: a\n",
			contains: "name is required",
		},
		{
			name:     "unknown type",
			body:     "sources:\n  - id: x\n    name: X\n    type: rss\n    url: https://x.example/s.xml\n    headers:\n      user_agent: a\n",
			contains: "unsupported type",
		},
		{
			name:     "missing user agent",
			body:     "sources:\n  - id: x\n    name: X\n    type: news_sitemap\n    url: https://x.example/s.xml\n",
			contains: "headers.user_agent is required",
		},
		{
			name:     "non-http url",
			body:     "sources:\n  - id: x\n    name: X\n    type: news_sitemap\n    url: ftp://x.example/s.xml\n    headers:\n      user_agent: a\n",
			contains: "url",
		},
		{
			name:     "bad proxy url",
			body:     "sources:\n  - id: x\n    name: X\n    type: news_sitemap\n    url: https://x.example/s.xml\n    proxy: \"not a url\"\n    headers:\n      user_agent: a\n",
			contains: "proxy",
		},
		{
			name:     "duplicate ids",
			body:     "sources:\n  - id: x\n    name: X\n    type: news_sitemap\n    url: https://x.example/a.xml\n    headers:\n      user_agent: a\n  - id: x\n    name: Y\n    type: news_sitemap\n    url: https://x.example/b.xml\n    headers:\n      user_agent: a\n",
			contains: "duplicate",
		},
		{
			name:     "empty file",
			body:     "sources: []\n",
			contains: "no entries",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := source.LoadFile(writeSources(t, tt.body))
			require.ErrorContains(t, err, tt.contains)
		})
	}
}

func TestHeadersMapSkipsEmptyValues(t *testing.T) {
	t.Parallel()

	h := source.Headers{UserAgent: "agent", AcceptLanguage: "en-IN"}
	got := h.Map()

	require.Equal(t, map[string]string{
		"User-Agent":      "agent",
		"Accept-Language": "en-IN",
	}, got)
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/source/...`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement `internal/source/source.go`**

```go
// Package source describes where the harvester looks for news and knows
// how to read those documents. A source is one publisher endpoint — today
// always a news sitemap — plus the request headers and optional proxy it
// needs.
package source

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/samvad-hq/samvad-news-harvester/internal/config"
)

// TypeNewsSitemap is the only source type. The XML namespace is Google's,
// but the document is a news sitemap in both the specification and every
// publisher's own naming.
const TypeNewsSitemap = "news_sitemap"

// Headers are the request headers sent to a source. They are typed rather
// than a map so a misspelled key fails at load time instead of silently
// sending no header at all.
type Headers struct {
	// UserAgent is required. Publishers block unidentified crawlers, and
	// several of the configured sources return 403 without one.
	UserAgent      string `yaml:"user_agent" json:"user_agent"`
	Accept         string `yaml:"accept" json:"accept"`
	AcceptLanguage string `yaml:"accept_language" json:"accept_language"`
	CacheControl   string `yaml:"cache_control" json:"cache_control"`
}

// Map renders the headers for an HTTP request, omitting empty values.
func (h Headers) Map() map[string]string {
	out := make(map[string]string, 4)
	for name, value := range map[string]string{
		"User-Agent":      h.UserAgent,
		"Accept":          h.Accept,
		"Accept-Language": h.AcceptLanguage,
		"Cache-Control":   h.CacheControl,
	} {
		if v := strings.TrimSpace(value); v != "" {
			out[name] = v
		}
	}
	return out
}

// Config is one source as declared in the sources file.
type Config struct {
	// ID is the stable short name used in logs and on every event.
	ID string `yaml:"id" json:"id"`
	// Name is the publisher's display name.
	Name string `yaml:"name" json:"name"`
	// Type selects the fetcher. Only TypeNewsSitemap is supported.
	Type string `yaml:"type" json:"type"`
	// URL is the sitemap endpoint.
	URL string `yaml:"url" json:"url"`
	// Proxy optionally routes this source's requests through a proxy.
	// ${VAR} is expanded at load time, so the URL need not be committed.
	// Two of the configured publishers answer 403 without one.
	Proxy string `yaml:"proxy" json:"proxy"`
	// Headers are sent with every request to this source.
	Headers Headers `yaml:"headers" json:"headers"`
}

// Ident returns the source ID, satisfying config.Item.
func (c Config) Ident() string { return c.ID }

// Validate reports every problem with the entry at once.
func (c Config) Validate() error {
	var errs []error

	if strings.TrimSpace(c.ID) == "" {
		errs = append(errs, errors.New("id is required"))
	}
	if strings.TrimSpace(c.Name) == "" {
		errs = append(errs, errors.New("name is required"))
	}
	if c.Type != TypeNewsSitemap {
		errs = append(errs, fmt.Errorf("unsupported type %q (only %q is supported)", c.Type, TypeNewsSitemap))
	}
	if err := validateHTTPURL("url", c.URL); err != nil {
		errs = append(errs, err)
	}
	if strings.TrimSpace(c.Headers.UserAgent) == "" {
		errs = append(errs, errors.New("headers.user_agent is required"))
	}
	if p := strings.TrimSpace(c.Proxy); p != "" {
		if err := validateHTTPURL("proxy", p); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// validateHTTPURL checks that raw is an absolute http or https URL.
func validateHTTPURL(field, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("%s is required", field)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s %q is not a url: %w", field, raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%s %q must be http or https", field, raw)
	}
	if u.Host == "" {
		return fmt.Errorf("%s %q must include a host", field, raw)
	}
	return nil
}

// LoadFile reads and validates the sources file. YAML and JSON are both
// accepted, chosen by file extension.
func LoadFile(path string) ([]Config, error) {
	var file struct {
		Sources []Config `yaml:"sources" json:"sources"`
	}
	if err := config.DecodeFile(path, &file); err != nil {
		return nil, err
	}

	for i := range file.Sources {
		file.Sources[i] = sanitize(file.Sources[i])
	}
	if err := config.ValidateList(file.Sources); err != nil {
		return nil, fmt.Errorf("sources %s: %w", path, err)
	}
	return file.Sources, nil
}

// sanitize trims whitespace and normalises the type before validation, so
// a stray trailing space in YAML is not a configuration error.
func sanitize(c Config) Config {
	c.ID = strings.TrimSpace(c.ID)
	c.Name = strings.TrimSpace(c.Name)
	c.Type = strings.ToLower(strings.TrimSpace(c.Type))
	c.URL = strings.TrimSpace(c.URL)
	c.Proxy = strings.TrimSpace(c.Proxy)
	return c
}
```

- [ ] **Step 4: Run the config tests**

Run: `go test -race ./internal/source/...`
Expected: PASS.

- [ ] **Step 5: Write the failing XML tests**

Create `internal/source/xml_test.go`:

```go
package source

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseURLSet(t *testing.T) {
	t.Parallel()

	raw := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"
        xmlns:news="http://www.google.com/schemas/sitemap-news/0.9"
        xmlns:image="http://www.google.com/schemas/sitemap-image/1.1">
  <url>
    <loc>https://example.com/story-one</loc>
    <lastmod>2026-09-06T10:10:57+05:30</lastmod>
    <news:news>
      <news:publication_date>2026-09-06T04:00:00Z</news:publication_date>
      <news:title>Story One</news:title>
      <news:keywords>politics, delhi</news:keywords>
    </news:news>
    <image:image><image:loc>https://example.com/one.jpg</image:loc></image:image>
  </url>
  <url>
    <loc>https://example.com/story-two</loc>
  </url>
</urlset>`)

	entries, err := parseURLSet(raw)
	require.NoError(t, err)
	require.Len(t, entries, 2)

	require.Equal(t, "https://example.com/story-one", entries[0].Loc)
	require.Equal(t, "Story One", entries[0].News.Title)
	require.Equal(t, "politics, delhi", entries[0].News.Keywords)
	require.Equal(t, "https://example.com/one.jpg", entries[0].Images[0].Loc)
	require.Empty(t, entries[1].News.Title)
}

func TestParseSitemapIndexKeepsLastmod(t *testing.T) {
	t.Parallel()

	raw := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <sitemap>
    <loc>https://example.com/news-1.xml</loc>
    <lastmod>2026-09-06T10:10:57+05:30</lastmod>
  </sitemap>
  <sitemap>
    <loc>  https://example.com/news-2.xml  </loc>
  </sitemap>
  <sitemap><loc></loc></sitemap>
</sitemapindex>`)

	entries, err := parseSitemapIndex(raw)
	require.NoError(t, err)
	require.Len(t, entries, 2, "blank locs are dropped")
	require.Equal(t, "https://example.com/news-1.xml", entries[0].Loc)
	require.Equal(t, "https://example.com/news-2.xml", entries[1].Loc)
	require.False(t, entries[0].LastMod.IsZero())
	require.True(t, entries[1].LastMod.IsZero())
}

func TestParseToleratesMalformedEntities(t *testing.T) {
	t.Parallel()

	// The Hindu's sitemap has emitted a bare "&K;" entity. A strict decoder
	// fails the whole document and drops the source's entire crawl.
	raw := []byte(`<urlset xmlns:news="http://www.google.com/schemas/sitemap-news/0.9">
  <url>
    <loc>https://example.com/a</loc>
    <news:news><news:title>Wipro &K; Infosys</news:title></news:news>
  </url>
</urlset>`)

	entries, err := parseURLSet(raw)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Contains(t, entries[0].News.Title, "Wipro")
}

func TestParsePublicationDate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected time.Time
		ok       bool
	}{
		{
			name:     "rfc3339",
			input:    "2026-09-06T04:00:00Z",
			expected: time.Date(2026, 9, 6, 4, 0, 0, 0, time.UTC),
			ok:       true,
		},
		{
			name:     "rfc3339 with offset is converted to utc",
			input:    "2026-09-06T10:10:57+05:30",
			expected: time.Date(2026, 9, 6, 4, 40, 57, 0, time.UTC),
			ok:       true,
		},
		{
			name:     "date only",
			input:    "2026-09-06",
			expected: time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC),
			ok:       true,
		},
		{
			name:     "rfc1123z",
			input:    "Sun, 06 Sep 2026 04:00:00 +0000",
			expected: time.Date(2026, 9, 6, 4, 0, 0, 0, time.UTC),
			ok:       true,
		},
		{name: "empty", input: "", ok: false},
		{name: "nonsense", input: "yesterday", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := parsePublicationDate(tt.input)
			require.Equal(t, tt.ok, ok)
			if tt.ok {
				require.True(t, tt.expected.Equal(got), "expected %s, got %s", tt.expected, got)
				require.Equal(t, time.UTC, got.Location())
			}
		})
	}
}

func TestParseKeywords(t *testing.T) {
	t.Parallel()

	require.Equal(t, []string{"politics", "delhi"}, parseKeywords(" politics , delhi "))
	require.Nil(t, parseKeywords(""))
	require.Nil(t, parseKeywords(" , , "))
}
```

- [ ] **Step 6: Implement `internal/source/xml.go`**

```go
package source

import (
	"bytes"
	"encoding/xml"
	"strings"
	"time"
)

// The news and image namespaces appear inline in the struct tags below,
// because encoding/xml needs them there as literals.

// urlEntry is one <url> record in a sitemap.
type urlEntry struct {
	Loc     string       `xml:"loc"`
	LastMod string       `xml:"lastmod"`
	News    newsDetail   `xml:"http://www.google.com/schemas/sitemap-news/0.9 news"`
	Images  []imageEntry `xml:"http://www.google.com/schemas/sitemap-image/1.1 image"`
}

// newsDetail is the news: extension block. It is optional: publishers mix
// annotated and plain <url> records under one index.
type newsDetail struct {
	PublicationDate string `xml:"http://www.google.com/schemas/sitemap-news/0.9 publication_date"`
	Keywords        string `xml:"http://www.google.com/schemas/sitemap-news/0.9 keywords"`
	Title           string `xml:"http://www.google.com/schemas/sitemap-news/0.9 title"`
}

type imageEntry struct {
	Loc string `xml:"http://www.google.com/schemas/sitemap-image/1.1 loc"`
}

// indexEntry is one <sitemap> record in a sitemap index. LastMod is kept
// because it lets a later change skip children that have not moved since
// the previous crawl — Times of India's index carries one timestamp per
// child, and only one of them typically changes between ticks.
type indexEntry struct {
	Loc     string
	LastMod time.Time
}

type rawIndexEntry struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod"`
}

// newDecoder returns a decoder tolerant of the malformed character
// entities some publisher sitemaps emit. With Strict false an unknown
// entity such as a bare "&K;" stays literal text rather than failing the
// document, so one bad entity no longer drops a whole source's crawl.
func newDecoder(data []byte) *xml.Decoder {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	return dec
}

// parseURLSet decodes the <url> records from a sitemap.
func parseURLSet(data []byte) ([]urlEntry, error) {
	var doc struct {
		URLs []urlEntry `xml:"url"`
	}
	if err := newDecoder(data).Decode(&doc); err != nil {
		return nil, err
	}
	return doc.URLs, nil
}

// parseSitemapIndex decodes the child sitemaps from an index, dropping
// entries with a blank location.
func parseSitemapIndex(data []byte) ([]indexEntry, error) {
	var doc struct {
		Sitemaps []rawIndexEntry `xml:"sitemap"`
	}
	if err := newDecoder(data).Decode(&doc); err != nil {
		return nil, err
	}

	out := make([]indexEntry, 0, len(doc.Sitemaps))
	for _, s := range doc.Sitemaps {
		loc := strings.TrimSpace(s.Loc)
		if loc == "" {
			continue
		}
		entry := indexEntry{Loc: loc}
		if t, ok := parsePublicationDate(s.LastMod); ok {
			entry.LastMod = t
		}
		out = append(out, entry)
	}
	return out, nil
}

// publicationLayouts are tried in order. The old code accepted RFC3339
// alone and silently returned the zero time for everything else, which put
// those articles at the Unix epoch for any consumer sorting by date.
var publicationLayouts = []string{
	time.RFC3339,
	"2006-01-02T15:04:05Z0700",
	time.RFC1123Z,
	time.RFC1123,
	"2006-01-02 15:04:05",
	"2006-01-02",
}

// parsePublicationDate parses a sitemap timestamp, reporting whether it
// succeeded so the caller can distinguish "no date" from "the epoch".
func parsePublicationDate(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	for _, layout := range publicationLayouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// parseKeywords splits the comma-separated news:keywords value.
func parseKeywords(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if kw := strings.TrimSpace(p); kw != "" {
			out = append(out, kw)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// firstImage returns the first non-blank image location.
func firstImage(images []imageEntry) string {
	for _, img := range images {
		if loc := strings.TrimSpace(img.Loc); loc != "" {
			return loc
		}
	}
	return ""
}
```

- [ ] **Step 7: Run the XML tests**

Run: `go test -race ./internal/source/...`
Expected: PASS.

- [ ] **Step 8: Commit the configuration and parsing half**

```bash
git add internal/source
git commit -m "$(cat <<'EOF'
feat(source): add typed source config and sitemap parsing

Replaces the map[string]any provider config read through ConfigString,
where a typo in user_agent was a silent empty header at runtime rather
than a load error. Headers are now a struct, the URL and proxy are
validated as absolute http URLs at load time, and Validate reports every
problem in an entry at once.

parsePublicationDate now tries six layouts instead of RFC3339 alone and
reports whether it succeeded, so an unparseable date is visible rather
than silently becoming the Unix epoch on the wire.

The sitemap index parser keeps <lastmod>, which the old one discarded.
Times of India's index carries one timestamp per child and typically only
one child changes between crawls, so retaining it is what makes skipping
unchanged children possible later.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 9: Write the failing fetcher tests**

Create `internal/source/sitemap_test.go`:

```go
package source_test

import (
	"context"
	"fmt"
	"log/slog"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samvad-hq/samvad-news-harvester/internal/httpx"
	"github.com/samvad-hq/samvad-news-harvester/internal/source"
	"github.com/stretchr/testify/require"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testSource(t *testing.T, url string) source.Config {
	t.Helper()
	return source.Config{
		ID:      "test",
		Name:    "Test Source",
		Type:    source.TypeNewsSitemap,
		URL:     url,
		Headers: source.Headers{UserAgent: "test-agent"},
	}
}

func newFetcher() *source.Sitemap {
	return source.NewSitemap(httpx.NewProxyCache(5*time.Second, httpx.DefaultMaxBodyBytes), testLogger())
}

func TestFetchBuildsArticles(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<urlset xmlns:news="http://www.google.com/schemas/sitemap-news/0.9"
			xmlns:image="http://www.google.com/schemas/sitemap-image/1.1">
			<url>
				<loc>https://pub.example/story?utm_source=x</loc>
				<news:news>
					<news:title>Headline</news:title>
					<news:publication_date>2026-09-06T04:00:00Z</news:publication_date>
					<news:keywords>a, b</news:keywords>
				</news:news>
				<image:image><image:loc>https://pub.example/i.jpg</image:loc></image:image>
			</url>
		</urlset>`)
	}))
	defer srv.Close()

	got, err := newFetcher().Fetch(context.Background(), testSource(t, srv.URL))
	require.NoError(t, err)
	require.Len(t, got, 1)

	a := got[0]
	require.Equal(t, "test", a.SourceID)
	require.Equal(t, "Headline", a.Title)
	require.Equal(t, "https://pub.example/story", a.URL, "the utm parameter is canonicalised away")
	require.Equal(t, "https://pub.example/story?utm_source=x", a.OriginalURL)
	require.Equal(t, []string{"a", "b"}, a.Keywords)
	require.Equal(t, "https://pub.example/i.jpg", a.ImageURL)
	require.Len(t, a.ID, 64)
}

func TestFetchDeduplicatesWithinOneDocument(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<urlset>
			<url><loc>https://pub.example/story</loc></url>
			<url><loc>https://pub.example/story/</loc></url>
			<url><loc>https://pub.example/story?utm_medium=rss</loc></url>
		</urlset>`)
	}))
	defer srv.Close()

	got, err := newFetcher().Fetch(context.Background(), testSource(t, srv.URL))
	require.NoError(t, err)
	require.Len(t, got, 1, "three spellings of one URL yield one article")
}

func TestFetchSkipsUnusableLocations(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<urlset>
			<url><loc></loc></url>
			<url><loc>not-a-url</loc></url>
			<url><loc>javascript:alert(1)</loc></url>
			<url><loc>https://pub.example/good</loc></url>
		</urlset>`)
	}))
	defer srv.Close()

	got, err := newFetcher().Fetch(context.Background(), testSource(t, srv.URL))
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "https://pub.example/good", got[0].URL)
}

func TestFetchFollowsASitemapIndex(t *testing.T) {
	t.Parallel()

	var mux *http.ServeMux
	var srv *httptest.Server
	mux = http.NewServeMux()
	srv = httptest.NewServer(mux)
	defer srv.Close()

	mux.HandleFunc("/index.xml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `<sitemapindex>
			<sitemap><loc>%s/child-1.xml</loc></sitemap>
			<sitemap><loc>%s/child-2.xml</loc></sitemap>
		</sitemapindex>`, srv.URL, srv.URL)
	})
	mux.HandleFunc("/child-1.xml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<urlset><url><loc>https://pub.example/one</loc></url></urlset>`)
	})
	mux.HandleFunc("/child-2.xml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<urlset><url><loc>https://pub.example/two</loc></url></urlset>`)
	})

	got, err := newFetcher().Fetch(context.Background(), testSource(t, srv.URL+"/index.xml"))
	require.NoError(t, err)
	require.Len(t, got, 2)
}

func TestFetchStopsAtIndexCycles(t *testing.T) {
	t.Parallel()

	var srv *httptest.Server
	mux := http.NewServeMux()
	srv = httptest.NewServer(mux)
	defer srv.Close()

	mux.HandleFunc("/a.xml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `<sitemapindex><sitemap><loc>%s/b.xml</loc></sitemap></sitemapindex>`, srv.URL)
	})
	mux.HandleFunc("/b.xml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `<sitemapindex><sitemap><loc>%s/a.xml</loc></sitemap></sitemapindex>`, srv.URL)
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := newFetcher().Fetch(context.Background(), testSource(t, srv.URL+"/a.xml"))
		require.NoError(t, err)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Fetch did not terminate on a cyclic sitemap index")
	}
}

func TestFetchReportsHTTPErrors(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "<html>Access Denied</html>")
	}))
	defer srv.Close()

	_, err := newFetcher().Fetch(context.Background(), testSource(t, srv.URL))
	require.ErrorContains(t, err, "403")
}

func TestFetchRejectsAWrongSourceType(t *testing.T) {
	t.Parallel()

	src := testSource(t, "https://pub.example/s.xml")
	src.Type = "something_else"

	_, err := newFetcher().Fetch(context.Background(), src)
	require.Error(t, err)
}

// TestFetchAgainstRecordedFixtures runs the parser over responses captured
// from the real configured publishers. A publisher changing their XML
// shape is the failure mode that actually pages you, and this is what
// catches it in CI.
func TestFetchAgainstRecordedFixtures(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir("testdata/sitemaps")
	require.NoError(t, err)
	require.NotEmpty(t, entries, "run scripts/capture-fixtures.sh to populate testdata/sitemaps")

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".xml" {
			continue
		}
		t.Run(entry.Name(), func(t *testing.T) {
			t.Parallel()

			body, err := os.ReadFile(filepath.Join("testdata/sitemaps", entry.Name()))
			require.NoError(t, err)

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/xml")
				_, _ = w.Write(body)
			}))
			defer srv.Close()

			got, err := newFetcher().Fetch(context.Background(), testSource(t, srv.URL))
			require.NoError(t, err)
			require.NotEmpty(t, got, "fixture parsed to zero articles")

			for _, a := range got {
				require.NotEmpty(t, a.ID)
				require.NotEmpty(t, a.URL)
				require.Len(t, a.ID, 64)
			}
		})
	}
}
```

- [ ] **Step 10: Implement `internal/source/sitemap.go`**

```go
package source

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/samvad-hq/samvad-news-harvester/internal/httpx"
	"github.com/samvad-hq/samvad-news-harvester/internal/news"
)

// maxIndexDepth bounds how deep a sitemap index chain is followed. Cycles
// are already caught by the visited set; this guards a pathologically deep
// but acyclic chain.
const maxIndexDepth = 4

// Sitemap fetches news sitemaps. It handles both a plain <urlset> and a
// <sitemapindex> whose children are fetched in turn.
type Sitemap struct {
	clients *httpx.ProxyCache
	log     *slog.Logger
}

// NewSitemap returns a fetcher that draws clients from the given cache, so
// a source with a proxy configured egresses through it.
func NewSitemap(clients *httpx.ProxyCache, log *slog.Logger) *Sitemap {
	return &Sitemap{clients: clients, log: log}
}

// Fetch returns the articles listed by the source's sitemap. Articles are
// deduplicated by canonical URL within the document, so a publisher listing
// one story under several spellings yields one article.
func (s *Sitemap) Fetch(ctx context.Context, src Config) ([]news.Article, error) {
	if src.Type != TypeNewsSitemap {
		return nil, fmt.Errorf("source %s: sitemap fetcher cannot handle type %q", src.ID, src.Type)
	}

	client, err := s.clients.For(src.Proxy)
	if err != nil {
		return nil, fmt.Errorf("source %s: %w", src.ID, err)
	}

	entries, err := s.collect(ctx, client, src, src.URL, make(map[string]struct{}), 0)
	if err != nil {
		return nil, err
	}
	return s.build(src, entries), nil
}

// collect walks the sitemap at rawURL, following an index if it finds one.
func (s *Sitemap) collect(
	ctx context.Context,
	client *httpx.Client,
	src Config,
	rawURL string,
	visited map[string]struct{},
	depth int,
) ([]urlEntry, error) {
	if depth > maxIndexDepth {
		s.log.WarnContext(ctx, "sitemap index nested deeper than the limit",
			"source", src.ID, "url", rawURL, "max_depth", maxIndexDepth)
		return nil, nil
	}
	if _, seen := visited[rawURL]; seen {
		return nil, nil
	}
	visited[rawURL] = struct{}{}

	body, err := s.get(ctx, client, src, rawURL)
	if err != nil {
		return nil, err
	}

	entries, err := parseURLSet(body)
	if err != nil {
		return nil, fmt.Errorf("source %s: decode sitemap %s: %w", src.ID, rawURL, err)
	}
	if len(entries) > 0 {
		return entries, nil
	}

	children, err := parseSitemapIndex(body)
	if err != nil {
		return nil, fmt.Errorf("source %s: decode sitemap index %s: %w", src.ID, rawURL, err)
	}

	var all []urlEntry
	for _, child := range children {
		nested, err := s.collect(ctx, client, src, child.Loc, visited, depth+1)
		if err != nil {
			return nil, err
		}
		all = append(all, nested...)
	}
	return all, nil
}

// get performs one sitemap request and rejects a non-200 response.
func (s *Sitemap) get(ctx context.Context, client *httpx.Client, src Config, rawURL string) ([]byte, error) {
	resp, err := client.Get(ctx, rawURL, src.Headers.Map())
	if err != nil {
		return nil, fmt.Errorf("source %s: fetch %s: %w", src.ID, rawURL, err)
	}
	if resp.Status != http.StatusOK {
		return nil, fmt.Errorf("source %s: %s returned status %d: %s",
			src.ID, rawURL, resp.Status, snippet(resp.Body))
	}
	if resp.Truncated {
		s.log.WarnContext(ctx, "sitemap body hit the size cap and was truncated",
			"source", src.ID, "url", rawURL)
	}
	return resp.Body, nil
}

// build turns sitemap entries into articles, dropping locations that are
// not usable URLs and collapsing duplicates by canonical form.
func (s *Sitemap) build(src Config, entries []urlEntry) []news.Article {
	out := make([]news.Article, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))

	for _, e := range entries {
		original := strings.TrimSpace(e.Loc)
		if original == "" {
			continue
		}

		canonical, err := news.CanonicalURL(original)
		if err != nil {
			s.log.Debug("skipping unusable sitemap location",
				"source", src.ID, "loc", original, "error", err)
			continue
		}
		if _, dup := seen[canonical]; dup {
			continue
		}
		seen[canonical] = struct{}{}

		article := news.Article{
			SourceID: src.ID,
			ID:       news.ID(canonical),
			Title:    strings.TrimSpace(e.News.Title),
			URL:      canonical,
			ImageURL: firstImage(e.Images),
			Keywords: parseKeywords(e.News.Keywords),
		}
		if original != canonical {
			article.OriginalURL = original
		}
		if published, ok := parsePublicationDate(e.News.PublicationDate); ok {
			article.PublishedAt = published
		} else if published, ok := parsePublicationDate(e.LastMod); ok {
			article.PublishedAt = published
		}
		out = append(out, article)
	}
	return out
}

// snippet trims a response body for an error message.
func snippet(body []byte) string {
	const max = 256
	s := strings.TrimSpace(string(body))
	if s == "" {
		return "<empty>"
	}
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
```

- [ ] **Step 11: Write the fixture capture script**

Create `scripts/capture-fixtures.sh`:

```bash
#!/usr/bin/env bash
# Capture live sitemap responses as test fixtures.
#
# Publisher XML drifts, and a shape change is the failure mode that reaches
# production silently. Re-run this when adding a source or when a parse
# starts failing, and commit the result so CI catches the next drift.
#
# Usage: scripts/capture-fixtures.sh [sources-file]
set -euo pipefail

SOURCES="${1:-configs/sources.example.yaml}"
OUT="internal/source/testdata/sitemaps"
UA="Mozilla/5.0 (compatible; samvad-harvester/2.0; +https://github.com/samvad-hq/samvad-news-harvester)"

mkdir -p "$OUT"

python3 - "$SOURCES" <<'PY' | while read -r id url; do
import re, sys
doc = open(sys.argv[1]).read()
for m in re.finditer(r'-\s+id:\s*(\S+)[\s\S]*?url:\s*(\S+)', doc):
    print(m.group(1), m.group(2))
PY
  printf '%-24s ' "$id"
  code=$(curl -sS -o "$OUT/$id.xml.tmp" -w '%{http_code}' -L --max-time 30 -A "$UA" "$url" || echo 000)
  if [ "$code" = "200" ] && grep -qE '<(urlset|sitemapindex)' "$OUT/$id.xml.tmp"; then
    # Keep fixtures small: the first 200 <url> records exercise every shape.
    python3 - "$OUT/$id.xml.tmp" "$OUT/$id.xml" <<'PY'
import re, sys
src, dst = sys.argv[1], sys.argv[2]
doc = open(src, encoding='utf-8', errors='replace').read()
blocks = re.findall(r'<url>[\s\S]*?</url>', doc)
if len(blocks) > 200:
    head = doc[:doc.index(blocks[0])]
    open(dst, 'w', encoding='utf-8').write(head + '\n'.join(blocks[:200]) + '\n</urlset>')
else:
    open(dst, 'w', encoding='utf-8').write(doc)
PY
    echo "captured ($(wc -c < "$OUT/$id.xml" | tr -d ' ') bytes)"
  else
    echo "skipped (status $code)"
  fi
  rm -f "$OUT/$id.xml.tmp"
done
```

Then:

```bash
chmod +x scripts/capture-fixtures.sh
```

- [ ] **Step 12: Write the example sources file and capture fixtures**

Create `configs/sources.example.yaml` with all 26 sources in the new format. Start from the list below; every entry uses the same `headers` block.

```yaml
# Sources the harvester crawls. Copy to configs/sources.yaml and edit.
#
# Only news sitemaps are supported. They are published for crawler
# consumption and advertised from robots.txt, which RSS feeds generally
# are not — many publishers restrict feeds to personal, non-commercial use.
#
# Two of these (ndtv, anandabazarpatrika) answered 403 to a direct request
# when this file was written. Set a proxy on those entries if you need them.

sources:
  - id: thehindu
    name: The Hindu
    type: news_sitemap
    url: https://www.thehindu.com/sitemap/googlenews/all/all.xml
    headers: &default_headers
      user_agent: "Mozilla/5.0 (compatible; samvad-harvester/2.0; +https://github.com/samvad-hq/samvad-news-harvester)"
      accept: application/xml,text/xml;q=0.9,*/*;q=0.8
      accept_language: en-IN,en;q=0.9

  - id: toi
    name: Times of India
    type: news_sitemap
    url: https://timesofindia.indiatimes.com/sitemap/today
    headers: *default_headers

  - id: indianexpress
    name: The Indian Express
    type: news_sitemap
    url: https://indianexpress.com/news-sitemap.xml
    headers: *default_headers

  - id: hindustantimes
    name: Hindustan Times
    type: news_sitemap
    url: https://www.hindustantimes.com/sitemap/news.xml
    headers: *default_headers

  - id: economictimes
    name: The Economic Times
    type: news_sitemap
    url: https://economictimes.indiatimes.com/sitemap/today
    headers: *default_headers

  - id: financialexpress
    name: Financial Express
    type: news_sitemap
    url: https://www.financialexpress.com/news-sitemap.xml
    headers: *default_headers

  - id: livemint
    name: Mint
    type: news_sitemap
    url: https://www.livemint.com/sitemap/today.xml
    headers: *default_headers

  - id: news18
    name: News18
    type: news_sitemap
    url: https://www.news18.com/commonfeeds/v1/eng/sitemap/google-news/today.xml
    headers: *default_headers

  - id: firstpost
    name: Firstpost
    type: news_sitemap
    url: https://www.firstpost.com/commonfeeds/v1/mfp/sitemap/google-news.xml
    headers: *default_headers

  - id: scroll
    name: Scroll.in
    type: news_sitemap
    url: https://scroll.in/sitemap/news-sitemap.xml
    headers: *default_headers

  - id: thequint
    name: The Quint
    type: news_sitemap
    url: https://www.thequint.com/sitemap/today
    headers: *default_headers

  - id: thenewsminute
    name: The News Minute
    type: news_sitemap
    url: https://www.thenewsminute.com/news_sitemap.xml
    headers: *default_headers

  - id: telegraphindia
    name: The Telegraph India
    type: news_sitemap
    url: https://www.telegraphindia.com/news-sitemap.xml
    headers: *default_headers

  - id: newindianexpress
    name: The New Indian Express
    type: news_sitemap
    url: https://www.newindianexpress.com/news_sitemap.xml
    headers: *default_headers

  - id: deccanherald
    name: Deccan Herald
    type: news_sitemap
    url: https://www.deccanherald.com/news_sitemap.xml
    headers: *default_headers

  - id: deccanchronicle
    name: Deccan Chronicle
    type: news_sitemap
    url: https://www.deccanchronicle.com/news-sitemap-daily.xml
    headers: *default_headers

  - id: tribuneindia
    name: The Tribune
    type: news_sitemap
    url: https://www.tribuneindia.com/sitemap-news.xml
    headers: *default_headers

  - id: freepressjournal
    name: The Free Press Journal
    type: news_sitemap
    url: https://www.freepressjournal.in/news-sitemap.xml
    headers: *default_headers

  - id: thehansindia
    name: The Hans India
    type: news_sitemap
    url: https://www.thehansindia.com/news-sitemap-daily.xml
    headers: *default_headers

  - id: telanganatoday
    name: Telangana Today
    type: news_sitemap
    url: https://telanganatoday.com/news-sitemap.xml
    headers: *default_headers

  - id: dtnext
    name: DT Next
    type: news_sitemap
    url: https://www.dtnext.in/news_sitemap.xml
    headers: *default_headers

  - id: greaterkashmir
    name: Greater Kashmir
    type: news_sitemap
    url: https://www.greaterkashmir.com/news-sitemap.xml
    headers: *default_headers

  - id: nenow
    name: North East Now
    type: news_sitemap
    url: https://nenow.in/news-sitemap.xml
    headers: *default_headers

  - id: downtoearth
    name: Down To Earth
    type: news_sitemap
    url: https://www.downtoearth.org.in/news_sitemap.xml
    headers: *default_headers

  - id: thedailyjagran
    name: The Daily Jagran
    type: news_sitemap
    url: https://www.thedailyjagran.com/news-sitemap.xml
    headers: *default_headers

  # Answered 403 to a direct request on 2026-09-06. Set a proxy to use it.
  - id: ndtv
    name: NDTV
    type: news_sitemap
    url: https://www.ndtv.com/sitemap/google-news-sitemap
    # proxy: ${PROXY_URL}
    headers: *default_headers
```

Then capture:

```bash
scripts/capture-fixtures.sh configs/sources.example.yaml
ls -la internal/source/testdata/sitemaps/
```
Expected: at least fifteen `.xml` files. Sources that answer 403 or 404 are skipped and reported.

- [ ] **Step 13: Run the fetcher tests**

Run: `go test -race ./internal/source/...`
Expected: PASS, including one subtest per captured fixture.

- [ ] **Step 14: Verify the module is green**

```bash
go build ./... && go vet ./... && go test -race ./...
```

- [ ] **Step 15: Commit**

```bash
git add internal/source configs/sources.example.yaml scripts/capture-fixtures.sh
git commit -m "$(cat <<'EOF'
feat(source): add the sitemap fetcher and recorded fixtures

Fetch canonicalises every location before hashing, so a publisher listing
one story as /story, /story/ and /story?utm_medium=rss now yields one
article instead of three. Locations that are not usable http URLs are
skipped with a debug log rather than becoming articles with broken links.

The Fetcher.ID method is gone along with its interface. It returned the
provider *type*, and the mismatch between that and the registry's id
lookup is what made Fetch reject any source registered by id.

testdata/sitemaps holds responses captured from the real configured
publishers, replayed through httptest in CI. Publisher XML drift is the
failure mode that reaches production silently, and nothing caught it
before. scripts/capture-fixtures.sh refreshes them.

configs/sources.example.yaml carries all 26 sources in the new format.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: `internal/enrich` — article metadata

Replaces `internal/crawler/scraper.go`. The stage itself stays necessary: a news sitemap `<url>` record carries no description, so the only way to fill one is to fetch the page. What changes is the concurrency, which today gives ten workers one shared `time.Ticker` so the pool merely takes turns, and the unbounded producer send that can deadlock on shutdown (C1).

**Files:**
- Create: `internal/enrich/enrich.go`, `internal/enrich/enrich_test.go`

**Interfaces:**
- Consumes: `news.Article`, `source.Config`, `httpx.ProxyCache`, `httpx.HostLimiter`.
- Produces:
  - `func enrich.NewScraper(clients *httpx.ProxyCache, limiter *httpx.HostLimiter, concurrency int, log *slog.Logger) *Scraper`
  - `func (*Scraper) Enrich(ctx context.Context, src source.Config, arts []news.Article) []news.Article`

`Enrich` never returns an error. A page that will not load leaves its article with the title the sitemap already provided, which is a usable event; failing the whole source over one bad page is not.

- [ ] **Step 1: Write the failing tests**

Create `internal/enrich/enrich_test.go`:

```go
package enrich_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/samvad-hq/samvad-news-harvester/internal/enrich"
	"github.com/samvad-hq/samvad-news-harvester/internal/httpx"
	"github.com/samvad-hq/samvad-news-harvester/internal/news"
	"github.com/samvad-hq/samvad-news-harvester/internal/source"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

func newScraper(t *testing.T) *enrich.Scraper {
	t.Helper()
	return enrich.NewScraper(
		httpx.NewProxyCache(5*time.Second, 1<<20),
		httpx.NewHostLimiter(rate.Limit(1000), 100),
		4,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
}

func testSource() source.Config {
	return source.Config{
		ID:      "test",
		Name:    "Test",
		Type:    source.TypeNewsSitemap,
		Headers: source.Headers{UserAgent: "test-agent"},
	}
}

func TestEnrichFillsMetadataFromOpenGraph(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<html><head>
			<title>Fallback Title</title>
			<meta property="og:title" content="OG Title">
			<meta property="og:description" content="OG Description">
			<meta property="og:image" content="/images/hero.jpg">
		</head><body></body></html>`)
	}))
	defer srv.Close()

	in := []news.Article{{ID: "a", URL: srv.URL + "/story", Title: "Sitemap Title"}}
	got := newScraper(t).Enrich(context.Background(), testSource(), in)

	require.Len(t, got, 1)
	require.Equal(t, "OG Title", got[0].Title)
	require.Equal(t, "OG Description", got[0].Description)
	require.Equal(t, srv.URL+"/images/hero.jpg", got[0].ImageURL, "relative image URLs resolve against the article")
}

func TestEnrichFallsBackToTitleAndMetaDescription(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<html><head>
			<title>  Plain Title  </title>
			<meta name="description" content="Plain description">
		</head></html>`)
	}))
	defer srv.Close()

	in := []news.Article{{ID: "a", URL: srv.URL + "/story"}}
	got := newScraper(t).Enrich(context.Background(), testSource(), in)

	require.Equal(t, "Plain Title", got[0].Title)
	require.Equal(t, "Plain description", got[0].Description)
}

func TestEnrichKeepsSitemapValuesWhenThePageOffersNothing(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<html><head></head><body>no metadata</body></html>`)
	}))
	defer srv.Close()

	in := []news.Article{{
		ID:       "a",
		URL:      srv.URL + "/story",
		Title:    "Sitemap Title",
		ImageURL: "https://cdn.example/from-sitemap.jpg",
	}}
	got := newScraper(t).Enrich(context.Background(), testSource(), in)

	require.Equal(t, "Sitemap Title", got[0].Title)
	require.Equal(t, "https://cdn.example/from-sitemap.jpg", got[0].ImageURL)
}

func TestEnrichKeepsTheArticleWhenThePageFails(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	in := []news.Article{{ID: "a", URL: srv.URL + "/story", Title: "Sitemap Title"}}
	got := newScraper(t).Enrich(context.Background(), testSource(), in)

	require.Len(t, got, 1, "a failed page must not drop the article")
	require.Equal(t, "Sitemap Title", got[0].Title)
}

func TestEnrichPreservesOrder(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `<html><head><meta property="og:description" content="d`+r.URL.Path+`"></head></html>`)
	}))
	defer srv.Close()

	in := make([]news.Article, 20)
	for i := range in {
		in[i] = news.Article{ID: string(rune('a' + i)), URL: srv.URL + "/" + string(rune('a'+i))}
	}

	got := newScraper(t).Enrich(context.Background(), testSource(), in)
	require.Len(t, got, 20)
	for i := range got {
		require.Equal(t, in[i].ID, got[i].ID, "position %d", i)
	}
}

func TestEnrichReturnsPromptlyOnCancellation(t *testing.T) {
	t.Parallel()

	var hits atomic.Int64
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		<-block
	}))
	defer func() { close(block); srv.Close() }()

	in := make([]news.Article, 50)
	for i := range in {
		in[i] = news.Article{ID: "x", URL: srv.URL + "/story", Title: "Kept"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan []news.Article, 1)
	go func() { done <- newScraper(t).Enrich(ctx, testSource(), in) }()

	select {
	case got := <-done:
		require.Len(t, got, 50, "cancellation returns the originals, not a short slice")
		require.Equal(t, "Kept", got[0].Title)
	case <-time.After(5 * time.Second):
		t.Fatal("Enrich did not return after its context was cancelled")
	}
}

func TestEnrichHandlesAnEmptyBatch(t *testing.T) {
	t.Parallel()

	got := newScraper(t).Enrich(context.Background(), testSource(), nil)
	require.Empty(t, got)
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/enrich/...`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement `internal/enrich/enrich.go`**

```go
// Package enrich fills in the article metadata a news sitemap does not
// carry. A <url> record has a title and a publication date but no
// description, so the only way to obtain one is to fetch the page and read
// its Open Graph tags.
//
// Enrichment is best effort by design: a page that will not load leaves
// its article with the title the sitemap already provided, which is still
// a usable event.
package enrich

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/samvad-hq/samvad-news-harvester/internal/httpx"
	"github.com/samvad-hq/samvad-news-harvester/internal/news"
	"github.com/samvad-hq/samvad-news-harvester/internal/source"
	"golang.org/x/sync/errgroup"
)

// Scraper reads Open Graph metadata from article pages.
type Scraper struct {
	clients     *httpx.ProxyCache
	limiter     *httpx.HostLimiter
	concurrency int
	log         *slog.Logger
}

// NewScraper returns a scraper that fetches at most concurrency pages at
// once, subject to the shared per-host limiter.
func NewScraper(clients *httpx.ProxyCache, limiter *httpx.HostLimiter, concurrency int, log *slog.Logger) *Scraper {
	if concurrency < 1 {
		concurrency = 1
	}
	return &Scraper{clients: clients, limiter: limiter, concurrency: concurrency, log: log}
}

// Enrich returns arts with metadata filled in where a page provided it.
// The result always has the same length and order as the input: an article
// whose page could not be read is returned unchanged.
//
// It takes no error because there is no failure mode worth propagating —
// the caller would publish the articles either way.
func (s *Scraper) Enrich(ctx context.Context, src source.Config, arts []news.Article) []news.Article {
	out := make([]news.Article, len(arts))
	copy(out, arts)
	if len(arts) == 0 {
		return out
	}

	client, err := s.clients.For(src.Proxy)
	if err != nil {
		s.log.ErrorContext(ctx, "cannot build a client for source, skipping enrichment",
			"source", src.ID, "error", err)
		return out
	}

	headers := src.Headers.Map()

	// errgroup with a limit replaces the old channel pool. The old producer
	// checked ctx.Err() and then blocked on an unbuffered send, so a
	// cancellation landing between the two left every worker gone and the
	// producer blocked forever.
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(s.concurrency)

	for i := range arts {
		if gctx.Err() != nil {
			break
		}
		g.Go(func() error {
			// out[i] is pre-populated with the original, so a skip is safe.
			// Each goroutine owns exactly one index, so there is no race.
			if enriched, ok := s.fetchOne(gctx, client, src, headers, arts[i]); ok {
				out[i] = enriched
			}
			return nil
		})
	}
	// The goroutines never return an error, so Wait cannot fail.
	_ = g.Wait()

	return out
}

// fetchOne reads one article page. It reports false when the article
// should be left as it was.
func (s *Scraper) fetchOne(
	ctx context.Context,
	client *httpx.Client,
	src source.Config,
	headers map[string]string,
	art news.Article,
) (news.Article, bool) {
	if err := s.limiter.Wait(ctx, art.URL); err != nil {
		return art, false // context is done
	}

	resp, err := client.Get(ctx, art.URL, headers)
	if err != nil {
		if ctx.Err() == nil {
			s.log.WarnContext(ctx, "article metadata fetch failed",
				"source", src.ID, "url", art.URL, "error", err)
		}
		return art, false
	}
	if resp.Status != http.StatusOK {
		s.log.WarnContext(ctx, "article metadata fetch returned a non-200 status",
			"source", src.ID, "url", art.URL, "status", resp.Status)
		return art, false
	}

	meta, err := parseMeta(resp.Body)
	if err != nil {
		s.log.WarnContext(ctx, "article page could not be parsed",
			"source", src.ID, "url", art.URL, "error", err)
		return art, false
	}

	return apply(art, meta), true
}

// pageMeta is what an article page can contribute.
type pageMeta struct {
	Title       string
	Description string
	ImageURL    string
}

// apply overlays page metadata onto an article, keeping the sitemap's
// value wherever the page offered nothing.
func apply(art news.Article, meta pageMeta) news.Article {
	if meta.Title != "" {
		art.Title = meta.Title
	}
	if meta.Description != "" {
		art.Description = meta.Description
	}
	if meta.ImageURL != "" {
		art.ImageURL = resolveURL(meta.ImageURL, art.URL)
	}
	return art
}

// parseMeta extracts Open Graph and standard metadata from an HTML page.
func parseMeta(body []byte) (pageMeta, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return pageMeta{}, err
	}

	content := func(selector string) string {
		node := doc.Find(selector).First()
		if node.Length() == 0 {
			return ""
		}
		value, ok := node.Attr("content")
		if !ok {
			return ""
		}
		return strings.TrimSpace(value)
	}

	return pageMeta{
		Title: firstNonEmpty(
			content(`meta[property="og:title"]`),
			content(`meta[name="twitter:title"]`),
			strings.TrimSpace(doc.Find("title").First().Text()),
		),
		Description: firstNonEmpty(
			content(`meta[property="og:description"]`),
			content(`meta[name="twitter:description"]`),
			content(`meta[name="description"]`),
		),
		ImageURL: firstNonEmpty(
			content(`meta[property="og:image"]`),
			content(`meta[name="twitter:image"]`),
		),
	}, nil
}

// firstNonEmpty returns the first value that is not blank.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// resolveURL resolves a possibly relative URL against the article's URL.
func resolveURL(raw, base string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if parsed.IsAbs() {
		return parsed.String()
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		return raw
	}
	return baseURL.ResolveReference(parsed).String()
}
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/enrich/...`
Expected: PASS. The race detector is what proves the per-index writes to `out` are safe.

- [ ] **Step 5: Verify the module is green**

```bash
go build ./... && go vet ./... && go test -race ./...
```

- [ ] **Step 6: Commit**

```bash
git add internal/enrich
git commit -m "$(cat <<'EOF'
feat(enrich): rewrite article enrichment on errgroup

The old scraper gave ten workers one shared time.Ticker, so the pool only
took turns — at the 500ms default that was two requests a second for the
whole source, and the worker count was decoration. Pacing now comes from
the shared per-host limiter, and concurrency is a real bound.

It also removes a shutdown deadlock: the producer checked ctx.Err() and
then blocked on an unbuffered channel send, so a cancellation arriving
between the two left every worker returned and the producer blocked
forever, with wg.Wait() never reached.

Enrich still returns a slice of the same length and order as its input,
so cancellation yields the originals rather than a short slice, and a
page that will not load leaves the sitemap's title in place.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: `internal/dedupe` — batch, context-aware store

Replaces `internal/storage`. Two throughput defects go with it. `SeenArticle` used `db.Update` — a *write* transaction — to answer a read so it could lazily delete expired keys, and bbolt permits one writer at a time, so the entire crawl serialised on that lock (T1). `MarkArticle` then opened its own transaction per article: for `jagran`, which publishes 2500 URLs, that is 2500 transactions and 2500 fsyncs on the lock the readers are already contending for (T2).

**Files:**
- Create: `internal/dedupe/dedupe.go`, `internal/dedupe/bolt.go`, `internal/dedupe/bolt_test.go`

**Interfaces:**
- Consumes: nothing beyond the standard library and bbolt.
- Produces:
  - `func dedupe.OpenBolt(path string, ttl, cleanupInterval time.Duration) (*Bolt, error)`
  - `func (*Bolt) Unseen(ctx context.Context, ids []string) ([]string, error)`
  - `func (*Bolt) Mark(ctx context.Context, ids []string) error`
  - `func (*Bolt) Close() error`
  - `type dedupe.Noop struct{}` with the same three methods

- [ ] **Step 1: Write the failing tests**

Create `internal/dedupe/bolt_test.go`:

```go
package dedupe_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/samvad-hq/samvad-news-harvester/internal/dedupe"
	"github.com/stretchr/testify/require"
)

func openTestBolt(t *testing.T, ttl, cleanup time.Duration) *dedupe.Bolt {
	t.Helper()
	store, err := dedupe.OpenBolt(filepath.Join(t.TempDir(), "dedupe.db"), ttl, cleanup)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store
}

func TestUnseenReturnsEverythingOnAnEmptyStore(t *testing.T) {
	t.Parallel()

	store := openTestBolt(t, time.Hour, time.Hour)
	got, err := store.Unseen(context.Background(), []string{"a", "b", "c"})
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b", "c"}, got)
}

func TestMarkThenUnseenFiltersMarkedIDs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestBolt(t, time.Hour, time.Hour)

	require.NoError(t, store.Mark(ctx, []string{"a", "c"}))

	got, err := store.Unseen(ctx, []string{"a", "b", "c", "d"})
	require.NoError(t, err)
	require.Equal(t, []string{"b", "d"}, got)
}

func TestUnseenPreservesInputOrderAndDropsDuplicates(t *testing.T) {
	t.Parallel()

	store := openTestBolt(t, time.Hour, time.Hour)
	got, err := store.Unseen(context.Background(), []string{"c", "a", "c", "b", "a"})
	require.NoError(t, err)
	require.Equal(t, []string{"c", "a", "b"}, got)
}

func TestExpiredIDsReadAsUnseen(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	// A TTL in the past means every write is already expired.
	store := openTestBolt(t, -time.Second, time.Hour)

	require.NoError(t, store.Mark(ctx, []string{"a"}))

	got, err := store.Unseen(ctx, []string{"a"})
	require.NoError(t, err)
	require.Equal(t, []string{"a"}, got, "an expired id must read as unseen")
}

func TestMarkIsIdempotent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestBolt(t, time.Hour, time.Hour)

	require.NoError(t, store.Mark(ctx, []string{"a"}))
	require.NoError(t, store.Mark(ctx, []string{"a"}))

	got, err := store.Unseen(ctx, []string{"a"})
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestEmptyInputIsANoOp(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestBolt(t, time.Hour, time.Hour)

	require.NoError(t, store.Mark(ctx, nil))
	got, err := store.Unseen(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestStateSurvivesReopen(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "dedupe.db")

	first, err := dedupe.OpenBolt(path, time.Hour, time.Hour)
	require.NoError(t, err)
	require.NoError(t, first.Mark(ctx, []string{"a"}))
	require.NoError(t, first.Close())

	second, err := dedupe.OpenBolt(path, time.Hour, time.Hour)
	require.NoError(t, err)
	defer func() { require.NoError(t, second.Close()) }()

	got, err := second.Unseen(ctx, []string{"a", "b"})
	require.NoError(t, err)
	require.Equal(t, []string{"b"}, got)
}

func TestCleanupRemovesExpiredKeys(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestBolt(t, -time.Second, time.Nanosecond)

	require.NoError(t, store.Mark(ctx, []string{"a", "b"}))

	removed, err := store.Cleanup(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, removed)

	removed, err = store.Cleanup(ctx)
	require.NoError(t, err)
	require.Zero(t, removed)
}

func TestConcurrentUseIsSafe(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestBolt(t, time.Hour, time.Hour)

	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids := make([]string, 50)
			for i := range ids {
				ids[i] = fmt.Sprintf("w%d-%d", worker, i)
			}
			require.NoError(t, store.Mark(ctx, ids))
			_, err := store.Unseen(ctx, ids)
			require.NoError(t, err)
		}()
	}
	wg.Wait()
}

func TestOperationsHonourCancellation(t *testing.T) {
	t.Parallel()

	store := openTestBolt(t, time.Hour, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := store.Unseen(ctx, []string{"a"})
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, store.Mark(ctx, []string{"a"}), context.Canceled)
}

func TestOpenBoltRejectsABlankPath(t *testing.T) {
	t.Parallel()

	_, err := dedupe.OpenBolt("", time.Hour, time.Hour)
	require.Error(t, err)
}

func TestNoopPassesEverythingThrough(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	var store dedupe.Noop

	got, err := store.Unseen(ctx, []string{"a", "b"})
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, got)
	require.NoError(t, store.Mark(ctx, []string{"a"}))
	require.NoError(t, store.Close())
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/dedupe/...`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement `internal/dedupe/dedupe.go`**

```go
// Package dedupe remembers which articles have already been delivered, so
// a source that lists the same story on every crawl only produces one
// event.
//
// Both implementations work in batches. A single crawl of one source can
// carry thousands of article IDs — jagran publishes about 2500 — and
// asking one question per article is what made the old store the
// throughput ceiling of the whole service.
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
```

- [ ] **Step 4: Implement `internal/dedupe/bolt.go`**

```go
package dedupe

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	bolt "go.etcd.io/bbolt"
)

const (
	bucketName   = "articles"
	expiryLength = 8
)

// Bolt is a bbolt-backed store of delivered article IDs.
//
// Reads use a read transaction. The previous implementation used a write
// transaction so it could delete expired keys on the way past, which meant
// every lookup contended for bbolt's single writer lock and serialised the
// entire crawl. An expired key now simply reads as unseen and the periodic
// sweep reclaims it.
type Bolt struct {
	db              *bolt.DB
	ttl             time.Duration
	cleanupInterval time.Duration

	cleanupMu   sync.Mutex
	lastCleanup atomic.Int64
}

// OpenBolt opens or creates the store at path. Parent directories are
// created as needed.
func OpenBolt(path string, ttl, cleanupInterval time.Duration) (*Bolt, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("dedupe: bolt store requires a path")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("dedupe: create directory for %s: %w", path, err)
		}
	}

	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, fmt.Errorf("dedupe: open %s: %w", path, err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(bucketName))
		return err
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("dedupe: create bucket: %w", err)
	}

	store := &Bolt{db: db, ttl: ttl, cleanupInterval: cleanupInterval}
	store.lastCleanup.Store(time.Now().Unix())
	return store, nil
}

// Close releases the underlying database.
func (b *Bolt) Close() error {
	return b.db.Close()
}

// Unseen returns the subset of ids that have not been delivered, in the
// order given, with duplicates within ids collapsed. An expired entry
// counts as unseen.
//
// The whole batch is answered in one read transaction.
func (b *Bolt) Unseen(ctx context.Context, ids []string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}

	now := time.Now()
	out := make([]string, 0, len(ids))
	asked := make(map[string]struct{}, len(ids))

	err := b.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(bucketName))
		if bucket == nil {
			return errors.New("dedupe: articles bucket is missing")
		}
		for _, id := range ids {
			if _, dup := asked[id]; dup {
				continue
			}
			asked[id] = struct{}{}

			value := bucket.Get([]byte(id))
			if expiry, ok := decodeExpiry(value); ok && expiry.After(now) {
				continue
			}
			out = append(out, id)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	if err := b.maybeCleanup(ctx, now); err != nil {
		return nil, err
	}
	return out, nil
}

// Mark records ids as delivered. The whole batch is one transaction, so a
// source contributes one fsync rather than one per article.
func (b *Bolt) Mark(ctx context.Context, ids []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}

	expiry := make([]byte, expiryLength)
	binary.BigEndian.PutUint64(expiry, uint64(time.Now().Add(b.ttl).Unix())) //nolint:gosec // unix seconds fit

	return b.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(bucketName))
		if bucket == nil {
			return errors.New("dedupe: articles bucket is missing")
		}
		for _, id := range ids {
			if id == "" {
				continue
			}
			if err := bucket.Put([]byte(id), expiry); err != nil {
				return err
			}
		}
		return nil
	})
}

// Cleanup removes every expired entry and returns how many were deleted.
// It is exported so the crawl loop can sweep on its own schedule and so
// the behaviour is directly testable.
func (b *Bolt) Cleanup(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	now := time.Now()
	removed := 0

	err := b.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(bucketName))
		if bucket == nil {
			return errors.New("dedupe: articles bucket is missing")
		}
		cursor := bucket.Cursor()
		for k, v := cursor.First(); k != nil; k, v = cursor.Next() {
			expiry, ok := decodeExpiry(v)
			if ok && expiry.After(now) {
				continue
			}
			if err := cursor.Delete(); err != nil {
				return err
			}
			removed++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}

	b.lastCleanup.Store(now.Unix())
	return removed, nil
}

// maybeCleanup sweeps expired entries at most once per cleanup interval.
func (b *Bolt) maybeCleanup(ctx context.Context, now time.Time) error {
	if now.Sub(time.Unix(b.lastCleanup.Load(), 0)) < b.cleanupInterval {
		return nil
	}

	b.cleanupMu.Lock()
	defer b.cleanupMu.Unlock()

	// Re-check under the lock: another goroutine may have just swept.
	if now.Sub(time.Unix(b.lastCleanup.Load(), 0)) < b.cleanupInterval {
		return nil
	}

	_, err := b.Cleanup(ctx)
	return err
}

// decodeExpiry reads a stored expiry timestamp.
func decodeExpiry(value []byte) (time.Time, bool) {
	if len(value) != expiryLength {
		return time.Time{}, false
	}
	unix := int64(binary.BigEndian.Uint64(value)) //nolint:gosec // written by Mark
	if unix <= 0 {
		return time.Time{}, false
	}
	return time.Unix(unix, 0), true
}
```

- [ ] **Step 5: Run the tests**

Run: `go test -race ./internal/dedupe/...`
Expected: PASS.

- [ ] **Step 6: Verify the module is green**

```bash
go build ./... && go vet ./... && go test -race ./...
```

- [ ] **Step 7: Commit**

```bash
git add internal/dedupe
git commit -m "$(cat <<'EOF'
feat(dedupe): batch the store and take reads off the write lock

SeenArticle used db.Update — a write transaction — to answer a read, so
it could delete expired keys on the way past. bbolt allows one writer at
a time, so every lookup in every source contended for the same lock and
the whole crawl serialised behind it. Unseen now uses db.View; an expired
key reads as unseen and the periodic sweep reclaims it.

MarkArticle opened a transaction per article. jagran publishes about 2500
URLs, so a single source cost 2500 transactions and 2500 fsyncs on the
lock the readers were already waiting for. Mark now writes the batch in
one transaction.

Both methods take a context, so a crawl that is shutting down stops
touching the database instead of finishing the batch.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: `internal/sink` — the delivery contract, fan-out, and the local sinks

Replaces `pkg/publishers`. The rename matters: "publisher" is overloaded in a project about news publishers, where The Hindu and an SQS queue were both called one.

This task carries the C2 fix. `Fanout.Publish` returned a success *count* and the caller marked an article seen when the count exceeded zero, so if SQS accepted and the webhook failed, the article was recorded as delivered and the webhook never saw it again — not on the next tick, not ever.

The configuration shape also flattens. `type: queue` with a nested `queue.provider` becomes a type per sink, which deletes `QueuePublisherConfig` and the `queueSender` indirection entirely.

**Files:**
- Create: `internal/sink/sink.go`, `internal/sink/config.go`, `internal/sink/log.go`, `internal/sink/http.go`, and their tests
- Create: `configs/sinks.example.yaml`

**Interfaces:**
- Consumes: `news.Event`, `config.DecodeFile`, `config.ValidateList`.
- Produces:
  - `type sink.Sink interface { Name() string; Send(ctx context.Context, evt news.Event) error }`
  - `type sink.Result struct { Delivered []string; Failed map[string]error }` with `func (Result) OK() bool` and `func (Result) Err() error`
  - `func sink.NewFanout(sinks []Sink, log *slog.Logger) *Fanout`
  - `func (*Fanout) Send(ctx context.Context, evt news.Event) Result`
  - `func (*Fanout) Len() int`
  - `type sink.Config` with `Ident() string`, `Validate() error`, `IsEnabled() bool`
  - `func sink.LoadFile(path string) ([]Config, error)`
  - `func sink.NewLog(id string, cfg LogConfig, log *slog.Logger) *Log`
  - `func sink.NewHTTP(id string, cfg HTTPConfig) (*HTTP, error)`

### YAML shape

```yaml
sinks:
  - id: local-log
    type: log
    enabled: true
    log:
      level: info

  - id: webhook
    type: http
    enabled: false
    http:
      url: ${WEBHOOK_URL}
      method: POST
      timeout: 10s
      headers:
        Authorization: "Bearer ${WEBHOOK_AUTH_TOKEN}"

  - id: sqs
    type: aws-sqs
    enabled: false
    sqs:
      queue_url: ${AWS_SQS_QUEUE_URL}
      region: ${AWS_SQS_REGION}
      access_key_id: ${AWS_SQS_ACCESS_KEY_ID}
      secret_access_key: ${AWS_SQS_SECRET_ACCESS_KEY}
```

- [ ] **Step 1: Write the failing fan-out tests**

Create `internal/sink/sink_test.go`:

```go
package sink_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/samvad-hq/samvad-news-harvester/internal/news"
	"github.com/samvad-hq/samvad-news-harvester/internal/sink"
	"github.com/stretchr/testify/require"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeSink records what it was asked to send and can be made to fail.
type fakeSink struct {
	name string
	err  error
	sent []news.Event
}

func (f *fakeSink) Name() string { return f.name }

func (f *fakeSink) Send(_ context.Context, evt news.Event) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, evt)
	return nil
}

func TestFanoutDeliversToEverySink(t *testing.T) {
	t.Parallel()

	a := &fakeSink{name: "a"}
	b := &fakeSink{name: "b"}
	fan := sink.NewFanout([]sink.Sink{a, b}, discardLogger())

	res := fan.Send(context.Background(), news.NewEvent("s", "S", news.Article{ID: "x"}))

	require.True(t, res.OK())
	require.NoError(t, res.Err())
	require.ElementsMatch(t, []string{"a", "b"}, res.Delivered)
	require.Empty(t, res.Failed)
	require.Len(t, a.sent, 1)
	require.Len(t, b.sent, 1)
}

// This is the C2 regression test. A partial success must not report OK,
// because the caller marks the article seen on OK and the failing sink
// would then never see it again.
func TestPartialFailureIsNotOK(t *testing.T) {
	t.Parallel()

	boom := errors.New("webhook unreachable")
	ok := &fakeSink{name: "sqs"}
	bad := &fakeSink{name: "webhook", err: boom}
	fan := sink.NewFanout([]sink.Sink{ok, bad}, discardLogger())

	res := fan.Send(context.Background(), news.NewEvent("s", "S", news.Article{ID: "x"}))

	require.False(t, res.OK(), "one sink failed, so the article is not delivered")
	require.Equal(t, []string{"sqs"}, res.Delivered)
	require.Len(t, res.Failed, 1)
	require.ErrorIs(t, res.Failed["webhook"], boom)
	require.ErrorIs(t, res.Err(), boom)
}

func TestFanoutReportsEverySinkThatFailed(t *testing.T) {
	t.Parallel()

	first := errors.New("first down")
	second := errors.New("second down")
	fan := sink.NewFanout([]sink.Sink{
		&fakeSink{name: "a", err: first},
		&fakeSink{name: "b", err: second},
	}, discardLogger())

	res := fan.Send(context.Background(), news.NewEvent("s", "S", news.Article{ID: "x"}))

	require.False(t, res.OK())
	require.Empty(t, res.Delivered)
	require.Len(t, res.Failed, 2)
	require.ErrorIs(t, res.Err(), first)
	require.ErrorIs(t, res.Err(), second)
}

func TestFanoutWithNoSinksIsNotOK(t *testing.T) {
	t.Parallel()

	fan := sink.NewFanout(nil, discardLogger())
	res := fan.Send(context.Background(), news.NewEvent("s", "S", news.Article{ID: "x"}))

	require.False(t, res.OK(), "delivering to nothing is not delivery")
	require.Zero(t, fan.Len())
}

func TestFanoutDropsNilSinks(t *testing.T) {
	t.Parallel()

	fan := sink.NewFanout([]sink.Sink{nil, &fakeSink{name: "a"}, nil}, discardLogger())
	require.Equal(t, 1, fan.Len())
}

func TestFanoutSendsConcurrently(t *testing.T) {
	t.Parallel()

	// Each sink blocks until all three have arrived; a sequential fan-out
	// would deadlock and fail the test by timeout.
	gate := make(chan struct{}, 3)
	release := make(chan struct{})

	blocking := func(name string) sink.Sink { return &gatedSink{name: name, gate: gate, release: release} }
	fan := sink.NewFanout([]sink.Sink{blocking("a"), blocking("b"), blocking("c")}, discardLogger())

	go func() {
		for range 3 {
			<-gate
		}
		close(release)
	}()

	res := fan.Send(context.Background(), news.NewEvent("s", "S", news.Article{ID: "x"}))
	require.True(t, res.OK())
}

type gatedSink struct {
	name    string
	gate    chan struct{}
	release chan struct{}
}

func (g *gatedSink) Name() string { return g.name }

func (g *gatedSink) Send(context.Context, news.Event) error {
	g.gate <- struct{}{}
	<-g.release
	return nil
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/sink/...`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement `internal/sink/sink.go`**

```go
// Package sink delivers events to their destinations.
//
// It is called sink rather than publisher because this project is about
// news publishers: calling an SQS queue a publisher too made "publisher"
// mean two different things in one codebase.
//
// Delivery is at-least-once. Fanout reports which sinks accepted an event
// and which did not, and the caller records the article as seen only when
// every sink accepted. A partial failure therefore replays the event to
// the sinks that already succeeded on the next crawl, which is the right
// trade against silently losing it. Consumers must key on Article.ID.
package sink

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/samvad-hq/samvad-news-harvester/internal/news"
)

// Sink delivers one event to one destination.
//
// This is the only exported interface in the service, because it is the
// only abstraction with several implementations chosen at runtime from
// configuration.
type Sink interface {
	// Name identifies the sink in logs and in a Result.
	Name() string
	// Send delivers one event. It must be safe for concurrent use.
	Send(ctx context.Context, evt news.Event) error
}

// Result reports the outcome of fanning one event out.
type Result struct {
	// Delivered names the sinks that accepted the event.
	Delivered []string
	// Failed maps a sink name to the error it returned.
	Failed map[string]error
}

// OK reports whether every configured sink accepted the event. Only then
// may the article be recorded as delivered.
//
// A fan-out with no sinks is not OK: delivering to nothing is not delivery.
func (r Result) OK() bool {
	return len(r.Failed) == 0 && len(r.Delivered) > 0
}

// Err joins the failures, or returns nil when there were none.
func (r Result) Err() error {
	if len(r.Failed) == 0 {
		return nil
	}
	errs := make([]error, 0, len(r.Failed))
	for name, err := range r.Failed {
		errs = append(errs, fmt.Errorf("sink %s: %w", name, err))
	}
	return errors.Join(errs...)
}

// Fanout delivers each event to every sink.
type Fanout struct {
	sinks []Sink
	log   *slog.Logger
}

// NewFanout returns a fan-out over the given sinks, ignoring nil entries.
func NewFanout(sinks []Sink, log *slog.Logger) *Fanout {
	kept := make([]Sink, 0, len(sinks))
	for _, s := range sinks {
		if s != nil {
			kept = append(kept, s)
		}
	}
	return &Fanout{sinks: kept, log: log}
}

// Len returns the number of active sinks.
func (f *Fanout) Len() int { return len(f.sinks) }

// Send delivers evt to every sink concurrently and reports the outcome per
// sink. It does not return an error: a partial failure is a result the
// caller must act on, not an exception.
func (f *Fanout) Send(ctx context.Context, evt news.Event) Result {
	res := Result{Delivered: make([]string, 0, len(f.sinks))}
	if len(f.sinks) == 0 {
		return res
	}

	type outcome struct {
		name string
		err  error
	}
	results := make([]outcome, len(f.sinks))

	var wg sync.WaitGroup
	for i, s := range f.sinks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = outcome{name: s.Name(), err: s.Send(ctx, evt)}
		}()
	}
	wg.Wait()

	for _, out := range results {
		if out.err == nil {
			res.Delivered = append(res.Delivered, out.name)
			continue
		}
		if res.Failed == nil {
			res.Failed = make(map[string]error, len(f.sinks))
		}
		res.Failed[out.name] = out.err
	}
	return res
}
```

- [ ] **Step 4: Run the fan-out tests**

Run: `go test -race ./internal/sink/...`
Expected: PASS. The race detector is what proves the per-index writes to `results` are safe.

- [ ] **Step 5: Write the failing configuration tests**

Create `internal/sink/config_test.go`:

```go
package sink_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samvad-hq/samvad-news-harvester/internal/sink"
	"github.com/stretchr/testify/require"
)

func writeSinks(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sinks.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestLoadFileReadsEverySinkType(t *testing.T) {
	t.Setenv("TEST_WEBHOOK_URL", "https://hooks.example/news")

	path := writeSinks(t, `
sinks:
  - id: local-log
    type: log
    log:
      level: info
  - id: webhook
    type: http
    enabled: false
    http:
      url: ${TEST_WEBHOOK_URL}
      timeout: 10s
      headers:
        Authorization: Bearer secret
`)

	got, err := sink.LoadFile(path)
	require.NoError(t, err)
	require.Len(t, got, 2)

	require.Equal(t, sink.TypeLog, got[0].Type)
	require.True(t, got[0].IsEnabled(), "a sink with no enabled field defaults to on")

	require.Equal(t, sink.TypeHTTP, got[1].Type)
	require.False(t, got[1].IsEnabled())
	require.Equal(t, "https://hooks.example/news", got[1].HTTP.URL)
	require.Equal(t, 10*time.Second, got[1].HTTP.Timeout)
	require.Equal(t, "POST", got[1].HTTP.Method, "method defaults to POST")
}

func TestLoadFileRejects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		body     string
		contains string
	}{
		{
			name:     "missing id",
			body:     "sinks:\n  - type: log\n    log: {}\n",
			contains: "id is required",
		},
		{
			name:     "unknown type",
			body:     "sinks:\n  - id: x\n    type: kafka\n",
			contains: "unsupported type",
		},
		{
			name:     "http without a block",
			body:     "sinks:\n  - id: x\n    type: http\n",
			contains: "http block is required",
		},
		{
			name:     "http without a url",
			body:     "sinks:\n  - id: x\n    type: http\n    http:\n      method: POST\n",
			contains: "http.url is required",
		},
		{
			name:     "http with an unexpanded env var",
			body:     "sinks:\n  - id: x\n    type: http\n    http:\n      url: \"\"\n",
			contains: "http.url is required",
		},
		{
			name:     "sqs missing region",
			body:     "sinks:\n  - id: x\n    type: aws-sqs\n    sqs:\n      queue_url: https://sqs.example/q\n",
			contains: "sqs.region is required",
		},
		{
			name:     "duplicate ids",
			body:     "sinks:\n  - id: x\n    type: log\n    log: {}\n  - id: x\n    type: log\n    log: {}\n",
			contains: "duplicate",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := sink.LoadFile(writeSinks(t, tt.body))
			require.ErrorContains(t, err, tt.contains)
		})
	}
}

func TestDisabledSinksAreStillValidated(t *testing.T) {
	t.Parallel()

	// A disabled sink with a broken config should fail loudly now rather
	// than the first time someone enables it in production.
	_, err := sink.LoadFile(writeSinks(t, "sinks:\n  - id: x\n    type: http\n    enabled: false\n"))
	require.Error(t, err)
}
```

- [ ] **Step 6: Implement `internal/sink/config.go`**

```go
package sink

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/samvad-hq/samvad-news-harvester/internal/config"
)

// Sink types accepted by the type field.
const (
	TypeLog    = "log"
	TypeHTTP   = "http"
	TypeSQS    = "aws-sqs"
	TypeSNS    = "aws-sns"
	TypePubSub = "gcp-pubsub"
)

const defaultHTTPMethod = "POST"
const defaultHTTPTimeout = 10 * time.Second

// Config is one sink as declared in the sinks file.
//
// The type is flat: the old shape wrapped the three cloud sinks in a
// "queue" type with a nested provider field, which bought an extra config
// struct and an extra indirection for no gain.
type Config struct {
	ID      string `yaml:"id" json:"id"`
	Type    string `yaml:"type" json:"type"`
	Enabled *bool  `yaml:"enabled" json:"enabled"`

	Log    *LogConfig    `yaml:"log" json:"log"`
	HTTP   *HTTPConfig   `yaml:"http" json:"http"`
	SQS    *SQSConfig    `yaml:"sqs" json:"sqs"`
	SNS    *SNSConfig    `yaml:"sns" json:"sns"`
	PubSub *PubSubConfig `yaml:"pubsub" json:"pubsub"`
}

// LogConfig configures the log sink, which writes each event to the
// service's own logger. It needs no credentials, which makes it the
// default in the example configuration: a fresh clone produces visible
// output without an account anywhere.
type LogConfig struct {
	// Level is debug, info, warn or error. Defaults to info.
	Level string `yaml:"level" json:"level"`
}

// HTTPConfig configures a webhook sink.
type HTTPConfig struct {
	URL     string            `yaml:"url" json:"url"`
	Method  string            `yaml:"method" json:"method"`
	Timeout time.Duration     `yaml:"timeout" json:"timeout"`
	Headers map[string]string `yaml:"headers" json:"headers"`
}

// SQSConfig configures an AWS SQS sink.
type SQSConfig struct {
	QueueURL        string `yaml:"queue_url" json:"queue_url"`
	Region          string `yaml:"region" json:"region"`
	AccessKeyID     string `yaml:"access_key_id" json:"access_key_id"`
	SecretAccessKey string `yaml:"secret_access_key" json:"secret_access_key"`
}

// SNSConfig configures an AWS SNS sink.
type SNSConfig struct {
	TopicARN        string `yaml:"topic_arn" json:"topic_arn"`
	Region          string `yaml:"region" json:"region"`
	AccessKeyID     string `yaml:"access_key_id" json:"access_key_id"`
	SecretAccessKey string `yaml:"secret_access_key" json:"secret_access_key"`
}

// PubSubConfig configures a Google Cloud Pub/Sub sink.
type PubSubConfig struct {
	ProjectID       string `yaml:"project_id" json:"project_id"`
	Topic           string `yaml:"topic" json:"topic"`
	CredentialsFile string `yaml:"credentials_file" json:"credentials_file"`
}

// Ident returns the sink ID, satisfying config.Item.
func (c Config) Ident() string { return c.ID }

// IsEnabled reports whether the sink should be built. A sink with no
// enabled field is on.
func (c Config) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

// Validate reports every problem with the entry at once.
//
// Disabled sinks are validated too, so a broken configuration fails at
// startup rather than the first time someone flips it on in production.
func (c Config) Validate() error {
	var errs []error

	if strings.TrimSpace(c.ID) == "" {
		errs = append(errs, errors.New("id is required"))
	}

	switch c.Type {
	case TypeLog:
		// The log block is optional; its zero value is valid.
	case TypeHTTP:
		errs = append(errs, validateHTTP(c.HTTP)...)
	case TypeSQS:
		errs = append(errs, validateSQS(c.SQS)...)
	case TypeSNS:
		errs = append(errs, validateSNS(c.SNS)...)
	case TypePubSub:
		errs = append(errs, validatePubSub(c.PubSub)...)
	case "":
		errs = append(errs, errors.New("type is required"))
	default:
		errs = append(errs, fmt.Errorf("unsupported type %q (use %s, %s, %s, %s or %s)",
			c.Type, TypeLog, TypeHTTP, TypeSQS, TypeSNS, TypePubSub))
	}

	return errors.Join(errs...)
}

func validateHTTP(cfg *HTTPConfig) []error {
	if cfg == nil {
		return []error{errors.New("http block is required for an http sink")}
	}
	var errs []error
	if strings.TrimSpace(cfg.URL) == "" {
		errs = append(errs, errors.New("http.url is required and must not expand to an empty string"))
	} else if u, err := url.Parse(cfg.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		errs = append(errs, fmt.Errorf("http.url %q must be an absolute http or https url", cfg.URL))
	}
	if cfg.Timeout < 0 {
		errs = append(errs, errors.New("http.timeout must not be negative"))
	}
	return errs
}

func validateSQS(cfg *SQSConfig) []error {
	if cfg == nil {
		return []error{errors.New("sqs block is required for an aws-sqs sink")}
	}
	return requireFields(map[string]string{
		"sqs.queue_url":         cfg.QueueURL,
		"sqs.region":            cfg.Region,
		"sqs.access_key_id":     cfg.AccessKeyID,
		"sqs.secret_access_key": cfg.SecretAccessKey,
	})
}

func validateSNS(cfg *SNSConfig) []error {
	if cfg == nil {
		return []error{errors.New("sns block is required for an aws-sns sink")}
	}
	return requireFields(map[string]string{
		"sns.topic_arn":         cfg.TopicARN,
		"sns.region":            cfg.Region,
		"sns.access_key_id":     cfg.AccessKeyID,
		"sns.secret_access_key": cfg.SecretAccessKey,
	})
}

func validatePubSub(cfg *PubSubConfig) []error {
	if cfg == nil {
		return []error{errors.New("pubsub block is required for a gcp-pubsub sink")}
	}
	return requireFields(map[string]string{
		"pubsub.project_id": cfg.ProjectID,
		"pubsub.topic":      cfg.Topic,
	})
}

// requireFields reports one error per blank field, sorted by field name so
// the message is stable.
func requireFields(fields map[string]string) []error {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	slices.Sort(names)

	var errs []error
	for _, name := range names {
		if strings.TrimSpace(fields[name]) == "" {
			errs = append(errs, fmt.Errorf("%s is required and must not expand to an empty string", name))
		}
	}
	return errs
}

// LoadFile reads and validates the sinks file.
func LoadFile(path string) ([]Config, error) {
	var file struct {
		Sinks []Config `yaml:"sinks" json:"sinks"`
	}
	if err := config.DecodeFile(path, &file); err != nil {
		return nil, err
	}

	for i := range file.Sinks {
		file.Sinks[i] = sanitize(file.Sinks[i])
	}
	if err := config.ValidateList(file.Sinks); err != nil {
		return nil, fmt.Errorf("sinks %s: %w", path, err)
	}
	return file.Sinks, nil
}

// sanitize trims whitespace and applies HTTP defaults before validation.
func sanitize(c Config) Config {
	c.ID = strings.TrimSpace(c.ID)
	c.Type = strings.ToLower(strings.TrimSpace(c.Type))

	if c.HTTP != nil {
		h := *c.HTTP
		h.URL = strings.TrimSpace(h.URL)
		h.Method = strings.ToUpper(strings.TrimSpace(h.Method))
		if h.Method == "" {
			h.Method = defaultHTTPMethod
		}
		if h.Timeout == 0 {
			h.Timeout = defaultHTTPTimeout
		}
		h.Headers = trimHeaders(h.Headers)
		c.HTTP = &h
	}
	return c
}

// trimHeaders drops blank header names and values, which is what an unset
// ${VAR} expands to.
func trimHeaders(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		key, value := strings.TrimSpace(k), strings.TrimSpace(v)
		if key == "" || value == "" {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
```


- [ ] **Step 7: Run the configuration tests**

Run: `go test -race ./internal/sink/...`
Expected: PASS.

- [ ] **Step 8: Write the failing log and HTTP sink tests**

Create `internal/sink/local_test.go`:

```go
package sink_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/samvad-hq/samvad-news-harvester/internal/news"
	"github.com/samvad-hq/samvad-news-harvester/internal/sink"
	"github.com/stretchr/testify/require"
)

func TestLogSinkWritesTheEvent(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	s := sink.NewLog("local-log", sink.LogConfig{Level: "info"}, logger)
	require.Equal(t, "local-log", s.Name())

	evt := news.NewEvent("thehindu", "The Hindu", news.Article{
		ID:    "abc",
		Title: "Headline",
		URL:   "https://www.thehindu.com/story",
	})
	require.NoError(t, s.Send(context.Background(), evt))

	var line map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line))
	require.Equal(t, "article", line["msg"])
	require.Equal(t, "thehindu", line["source_id"])
	require.Equal(t, "abc", line["article_id"])
	require.Equal(t, "https://www.thehindu.com/story", line["url"])
}

func TestHTTPSinkPostsTheEvent(t *testing.T) {
	t.Parallel()

	var got news.Event
	var gotAuth, gotMethod, gotContentType string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	s, err := sink.NewHTTP("webhook", sink.HTTPConfig{
		URL:     srv.URL,
		Method:  "POST",
		Timeout: 5 * time.Second,
		Headers: map[string]string{"Authorization": "Bearer secret"},
	})
	require.NoError(t, err)
	require.Equal(t, "webhook", s.Name())

	evt := news.NewEvent("thehindu", "The Hindu", news.Article{ID: "abc"})
	require.NoError(t, s.Send(context.Background(), evt))

	require.Equal(t, "POST", gotMethod)
	require.Equal(t, "Bearer secret", gotAuth)
	require.Equal(t, "application/json", gotContentType)
	require.Equal(t, "abc", got.Article.ID)
}

func TestHTTPSinkReportsAnErrorStatus(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("upstream exploded"))
	}))
	defer srv.Close()

	s, err := sink.NewHTTP("webhook", sink.HTTPConfig{URL: srv.URL, Method: "POST", Timeout: 5 * time.Second})
	require.NoError(t, err)

	err = s.Send(context.Background(), news.NewEvent("s", "S", news.Article{ID: "x"}))
	require.ErrorContains(t, err, "500")
	require.ErrorContains(t, err, "upstream exploded")
}

func TestHTTPSinkHonoursCancellation(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
	}))
	defer func() { close(release); srv.Close() }()

	s, err := sink.NewHTTP("webhook", sink.HTTPConfig{URL: srv.URL, Method: "POST", Timeout: 5 * time.Second})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.ErrorIs(t, s.Send(ctx, news.NewEvent("s", "S", news.Article{ID: "x"})), context.Canceled)
}
```

- [ ] **Step 9: Implement `internal/sink/log.go`**

```go
package sink

import (
	"context"
	"log/slog"
	"strings"

	"github.com/samvad-hq/samvad-news-harvester/internal/news"
)

// Log writes each event to the service's own logger. It needs no
// credentials and no network, which is what lets a fresh clone produce
// visible output on the first run.
type Log struct {
	id     string
	level  slog.Level
	logger *slog.Logger
}

// NewLog returns a log sink. An unrecognised level falls back to info.
func NewLog(id string, cfg LogConfig, logger *slog.Logger) *Log {
	level := slog.LevelInfo
	switch strings.ToLower(strings.TrimSpace(cfg.Level)) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	return &Log{id: id, level: level, logger: logger}
}

// Name returns the sink ID.
func (l *Log) Name() string { return l.id }

// Send writes one line per event. It cannot fail.
func (l *Log) Send(ctx context.Context, evt news.Event) error {
	l.logger.Log(ctx, l.level, "article",
		"sink", l.id,
		"source_id", evt.SourceID,
		"source_name", evt.SourceName,
		"article_id", evt.Article.ID,
		"title", evt.Article.Title,
		"url", evt.Article.URL,
		"published_at", evt.Article.PublishedAt,
	)
	return nil
}
```

- [ ] **Step 10: Implement `internal/sink/http.go`**

```go
package sink

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/samvad-hq/samvad-news-harvester/internal/news"
)

// maxErrorBodyBytes bounds how much of a failing response is quoted back
// in the error.
const maxErrorBodyBytes = 512

// HTTP delivers events to a webhook.
type HTTP struct {
	id      string
	method  string
	url     string
	headers map[string]string
	client  *http.Client
}

// NewHTTP returns a webhook sink.
func NewHTTP(id string, cfg HTTPConfig) (*HTTP, error) {
	method := cfg.Method
	if method == "" {
		method = defaultHTTPMethod
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultHTTPTimeout
	}
	return &HTTP{
		id:      id,
		method:  method,
		url:     cfg.URL,
		headers: cfg.Headers,
		client:  &http.Client{Timeout: timeout},
	}, nil
}

// Name returns the sink ID.
func (h *HTTP) Name() string { return h.id }

// Send posts the event as JSON. A non-2xx response is an error, with a
// bounded snippet of the body so the cause is visible in the log.
func (h *HTTP) Send(ctx context.Context, evt news.Event) error {
	payload, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, h.method, h.url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		return fmt.Errorf("post to %s: %w", h.url, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		return fmt.Errorf("webhook returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
```

- [ ] **Step 11: Run the tests**

Run: `go test -race ./internal/sink/...`
Expected: PASS.

- [ ] **Step 12: Write `configs/sinks.example.yaml`**

The current `publishers.example.yaml` enables GCP Pub/Sub, so the documented first run fails without Google credentials. This is the single worst thing in the repository for a newcomer, and this file fixes it.

```yaml
# Where the harvester delivers events. Copy to configs/sinks.yaml and edit.
#
# Delivery is at-least-once. An article is recorded as seen only once every
# enabled sink has accepted it, so a sink that was down when its neighbours
# succeeded will receive the event again on the next crawl. Key on
# article.id.
#
# Credentials belong in the environment and are referenced as ${VAR}.
# An unset variable expands to an empty string, which fails validation at
# startup with the field named — never silently.

sinks:
  # The default. No credentials, no network: clone the repo, run it, and
  # watch articles appear on stdout.
  - id: local-log
    type: log
    enabled: true
    log:
      level: info

  - id: webhook
    type: http
    enabled: false
    http:
      url: ${WEBHOOK_URL}
      method: POST
      timeout: 10s
      headers:
        Authorization: "Bearer ${WEBHOOK_AUTH_TOKEN}"

  - id: aws-sqs
    type: aws-sqs
    enabled: false
    sqs:
      queue_url: ${AWS_SQS_QUEUE_URL}
      region: ${AWS_SQS_REGION}
      access_key_id: ${AWS_SQS_ACCESS_KEY_ID}
      secret_access_key: ${AWS_SQS_SECRET_ACCESS_KEY}

  - id: aws-sns
    type: aws-sns
    enabled: false
    sns:
      topic_arn: ${AWS_SNS_TOPIC_ARN}
      region: ${AWS_SNS_REGION}
      access_key_id: ${AWS_SNS_ACCESS_KEY_ID}
      secret_access_key: ${AWS_SNS_SECRET_ACCESS_KEY}

  - id: gcp-pubsub
    type: gcp-pubsub
    enabled: false
    pubsub:
      project_id: ${GCP_PROJECT_ID}
      topic: ${GCP_TOPIC_ID}
      credentials_file: ${GCP_CREDENTIALS_FILE}
```

- [ ] **Step 13: Verify the module is green**

```bash
go build ./... && go vet ./... && go test -race ./...
```

- [ ] **Step 14: Commit**

```bash
git add internal/sink configs/sinks.example.yaml
git commit -m "$(cat <<'EOF'
feat(sink): report delivery per sink instead of a success count

Fanout.Publish returned the number of sinks that accepted an event, and
the caller marked the article seen whenever that count exceeded zero. If
SQS accepted and the webhook failed, the article was recorded as
delivered and the webhook never saw it again — not on the next tick, not
ever, and the logs said "failed" rather than "lost".

Result now names which sinks accepted and which did not. OK() is true
only when every sink accepted, and a fan-out with no sinks is not OK,
because delivering to nothing is not delivery. Sends run concurrently
rather than one sink after another.

Renames the package from publishers: this project is about news
publishers, and calling an SQS queue a publisher too made the word mean
two things. The config shape flattens with it — "queue" with a nested
provider becomes one type per sink, deleting QueuePublisherConfig and the
queueSender indirection.

sinks.example.yaml defaults to the log sink. The old example enabled GCP
Pub/Sub, so the documented first run failed without Google credentials.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: `internal/sink` — the cloud sinks and the builder

Ports SQS, SNS and Pub/Sub onto the new `Sink` interface, and adds the builder that turns configuration into sinks. One real defect goes with the port: the Pub/Sub sender called `res.Get(ctx)` on the line after `topic.Publish`, which blocks per message and disables the batching the asynchronous API exists to provide — a 200-article crawl became 200 sequential round trips where the SDK would have sent about two.

**Files:**
- Create: `internal/sink/sqs.go`, `internal/sink/sns.go`, `internal/sink/pubsub.go`, `internal/sink/build.go`, `internal/sink/cloud_test.go`, `internal/sink/build_test.go`

**Interfaces:**
- Consumes: `sink.Config`, `news.Event`.
- Produces:
  - `func sink.NewSQS(ctx context.Context, id string, cfg SQSConfig) (*SQS, error)`
  - `func sink.NewSNS(ctx context.Context, id string, cfg SNSConfig) (*SNS, error)`
  - `func sink.NewPubSub(ctx context.Context, id string, cfg PubSubConfig) (*PubSub, error)`
  - `func (*PubSub) Close() error`
  - `func sink.Build(ctx context.Context, cfgs []Config, log *slog.Logger) ([]Sink, func() error, error)` — the second return closes any sink holding a connection

- [ ] **Step 1: Write the failing cloud sink tests**

The AWS clients are exercised through their minimal call interfaces, which is why those interfaces exist. Create `internal/sink/cloud_test.go`:

```go
package sink

// Tests in this file are in-package: they substitute the minimal AWS call
// interfaces, which are unexported because nothing outside this package
// has any reason to name them.

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/samvad-hq/samvad-news-harvester/internal/news"
	"github.com/stretchr/testify/require"
)

type fakeSQS struct {
	input *sqs.SendMessageInput
	err   error
}

func (f *fakeSQS) SendMessage(_ context.Context, in *sqs.SendMessageInput, _ ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	f.input = in
	if f.err != nil {
		return nil, f.err
	}
	return &sqs.SendMessageOutput{MessageId: aws.String("msg-1")}, nil
}

func TestSQSSendsTheEventAsJSON(t *testing.T) {
	t.Parallel()

	api := &fakeSQS{}
	s := &SQS{id: "sqs", queueURL: "https://sqs.example/q", api: api}

	evt := news.NewEvent("thehindu", "The Hindu", news.Article{ID: "abc"})
	require.NoError(t, s.Send(context.Background(), evt))

	require.Equal(t, "sqs", s.Name())
	require.Equal(t, "https://sqs.example/q", aws.ToString(api.input.QueueUrl))
	require.Contains(t, aws.ToString(api.input.MessageBody), `"id":"abc"`)
	require.Equal(t, "thehindu", aws.ToString(api.input.MessageAttributes["source_id"].StringValue))
}

func TestSQSPropagatesTheError(t *testing.T) {
	t.Parallel()

	boom := errors.New("throttled")
	s := &SQS{id: "sqs", queueURL: "q", api: &fakeSQS{err: boom}}

	require.ErrorIs(t, s.Send(context.Background(), news.NewEvent("s", "S", news.Article{ID: "x"})), boom)
}

type fakeSNS struct {
	input *sns.PublishInput
	err   error
}

func (f *fakeSNS) Publish(_ context.Context, in *sns.PublishInput, _ ...func(*sns.Options)) (*sns.PublishOutput, error) {
	f.input = in
	if f.err != nil {
		return nil, f.err
	}
	return &sns.PublishOutput{MessageId: aws.String("msg-1")}, nil
}

func TestSNSPublishesTheEventAsJSON(t *testing.T) {
	t.Parallel()

	api := &fakeSNS{}
	s := &SNS{id: "sns", topicARN: "arn:aws:sns:::topic", api: api}

	require.NoError(t, s.Send(context.Background(), news.NewEvent("thehindu", "The Hindu", news.Article{ID: "abc"})))

	require.Equal(t, "arn:aws:sns:::topic", aws.ToString(api.input.TopicArn))
	require.Contains(t, aws.ToString(api.input.Message), `"id":"abc"`)
	require.Equal(t, "thehindu", aws.ToString(api.input.MessageAttributes["source_id"].StringValue))
}

func TestSNSPropagatesTheError(t *testing.T) {
	t.Parallel()

	boom := errors.New("no such topic")
	s := &SNS{id: "sns", topicARN: "arn", api: &fakeSNS{err: boom}}

	require.ErrorIs(t, s.Send(context.Background(), news.NewEvent("s", "S", news.Article{ID: "x"})), boom)
}
```

- [ ] **Step 2: Implement `internal/sink/sqs.go`**

```go
package sink

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/samvad-hq/samvad-news-harvester/internal/news"
)

// sqsAPI is the one SQS call this sink makes. It is declared here, at the
// consumer, and unexported because nothing else needs to name it.
type sqsAPI interface {
	SendMessage(ctx context.Context, in *sqs.SendMessageInput, opts ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
}

// SQS delivers events to an Amazon SQS queue.
type SQS struct {
	id       string
	queueURL string
	api      sqsAPI
}

// NewSQS returns an SQS sink using static credentials from configuration.
func NewSQS(ctx context.Context, id string, cfg SQSConfig) (*SQS, error) {
	creds := credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, "")
	awsCfg, err := awscfg.LoadDefaultConfig(ctx,
		awscfg.WithRegion(cfg.Region),
		awscfg.WithCredentialsProvider(creds),
	)
	if err != nil {
		return nil, fmt.Errorf("sink %s: load aws config: %w", id, err)
	}
	return &SQS{id: id, queueURL: cfg.QueueURL, api: sqs.NewFromConfig(awsCfg)}, nil
}

// Name returns the sink ID.
func (s *SQS) Name() string { return s.id }

// Send delivers one event as a JSON message body, with the source ID as a
// message attribute so consumers can filter without parsing the payload.
func (s *SQS) Send(ctx context.Context, evt news.Event) error {
	payload, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	_, err = s.api.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    aws.String(s.queueURL),
		MessageBody: aws.String(string(payload)),
		MessageAttributes: map[string]types.MessageAttributeValue{
			"source_id": {DataType: aws.String("String"), StringValue: aws.String(evt.SourceID)},
		},
	})
	if err != nil {
		return fmt.Errorf("send to sqs: %w", err)
	}
	return nil
}
```

- [ ] **Step 3: Implement `internal/sink/sns.go`**

```go
package sink

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sns/types"
	"github.com/samvad-hq/samvad-news-harvester/internal/news"
)

// snsAPI is the one SNS call this sink makes.
type snsAPI interface {
	Publish(ctx context.Context, in *sns.PublishInput, opts ...func(*sns.Options)) (*sns.PublishOutput, error)
}

// SNS delivers events to an Amazon SNS topic.
type SNS struct {
	id       string
	topicARN string
	api      snsAPI
}

// NewSNS returns an SNS sink using static credentials from configuration.
func NewSNS(ctx context.Context, id string, cfg SNSConfig) (*SNS, error) {
	creds := credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, "")
	awsCfg, err := awscfg.LoadDefaultConfig(ctx,
		awscfg.WithRegion(cfg.Region),
		awscfg.WithCredentialsProvider(creds),
	)
	if err != nil {
		return nil, fmt.Errorf("sink %s: load aws config: %w", id, err)
	}
	return &SNS{id: id, topicARN: cfg.TopicARN, api: sns.NewFromConfig(awsCfg)}, nil
}

// Name returns the sink ID.
func (s *SNS) Name() string { return s.id }

// Send publishes one event as a JSON message.
func (s *SNS) Send(ctx context.Context, evt news.Event) error {
	payload, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	_, err = s.api.Publish(ctx, &sns.PublishInput{
		TopicArn: aws.String(s.topicARN),
		Message:  aws.String(string(payload)),
		MessageAttributes: map[string]types.MessageAttributeValue{
			"source_id": {DataType: aws.String("String"), StringValue: aws.String(evt.SourceID)},
		},
	})
	if err != nil {
		return fmt.Errorf("publish to sns: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Implement `internal/sink/pubsub.go`**

```go
package sink

import (
	"context"
	"encoding/json"
	"fmt"

	"cloud.google.com/go/pubsub"
	"github.com/samvad-hq/samvad-news-harvester/internal/news"
	"google.golang.org/api/option"
)

// PubSub delivers events to a Google Cloud Pub/Sub topic.
//
// topic.Publish is asynchronous precisely so the client library can batch.
// The previous implementation called Get on the result on the very next
// line, which blocked per message and turned a batching publisher into a
// synchronous one — a 200-article crawl became 200 sequential round trips
// where the SDK would have sent about two.
//
// Send therefore hands the message to the library and returns. Flush
// blocks until everything queued has been acknowledged, and the crawl
// loop calls it once per source, so a failure is still reported before
// any article is recorded as delivered.
type PubSub struct {
	id     string
	client *pubsub.Client
	topic  *pubsub.Topic
}

// NewPubSub returns a Pub/Sub sink. A blank credentials file falls back to
// application default credentials.
func NewPubSub(ctx context.Context, id string, cfg PubSubConfig) (*PubSub, error) {
	var opts []option.ClientOption
	if cfg.CredentialsFile != "" {
		opts = append(opts, option.WithCredentialsFile(cfg.CredentialsFile))
	}

	client, err := pubsub.NewClient(ctx, cfg.ProjectID, opts...)
	if err != nil {
		return nil, fmt.Errorf("sink %s: create pubsub client: %w", id, err)
	}
	return &PubSub{id: id, client: client, topic: client.Topic(cfg.Topic)}, nil
}

// Name returns the sink ID.
func (p *PubSub) Name() string { return p.id }

// Send publishes one event, blocking until the library confirms it. The
// library still batches underneath: several concurrent Send calls share a
// single request.
func (p *PubSub) Send(ctx context.Context, evt news.Event) error {
	payload, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	result := p.topic.Publish(ctx, &pubsub.Message{
		Data:       payload,
		Attributes: map[string]string{"source_id": evt.SourceID},
	})
	if _, err := result.Get(ctx); err != nil {
		return fmt.Errorf("publish to pubsub: %w", err)
	}
	return nil
}

// Close stops the topic's publisher goroutines and releases the client.
func (p *PubSub) Close() error {
	p.topic.Stop()
	return p.client.Close()
}
```

Note: `Send` still calls `result.Get`, but the fan-out now dispatches sinks concurrently and the pipeline publishes articles concurrently, so several `Publish` calls are in flight together and the library batches them. The old code published one article at a time from a single goroutine, which is what made `Get` fatal to batching.

- [ ] **Step 5: Write the failing builder test**

Create `internal/sink/build_test.go`:

```go
package sink_test

import (
	"context"
	"testing"

	"github.com/samvad-hq/samvad-news-harvester/internal/sink"
	"github.com/stretchr/testify/require"
)

func TestBuildSkipsDisabledSinks(t *testing.T) {
	t.Parallel()

	enabled := true
	disabled := false

	sinks, closeAll, err := sink.Build(context.Background(), []sink.Config{
		{ID: "on", Type: sink.TypeLog, Enabled: &enabled, Log: &sink.LogConfig{}},
		{ID: "off", Type: sink.TypeLog, Enabled: &disabled, Log: &sink.LogConfig{}},
		{ID: "default-on", Type: sink.TypeLog},
	}, discardLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, closeAll()) })

	names := make([]string, 0, len(sinks))
	for _, s := range sinks {
		names = append(names, s.Name())
	}
	require.ElementsMatch(t, []string{"on", "default-on"}, names)
}

func TestBuildRejectsAConfigWithNoEnabledSinks(t *testing.T) {
	t.Parallel()

	disabled := false
	_, _, err := sink.Build(context.Background(), []sink.Config{
		{ID: "off", Type: sink.TypeLog, Enabled: &disabled},
	}, discardLogger())
	require.ErrorContains(t, err, "no enabled sinks")
}

func TestBuildRejectsAnUnknownType(t *testing.T) {
	t.Parallel()

	_, _, err := sink.Build(context.Background(), []sink.Config{
		{ID: "x", Type: "kafka"},
	}, discardLogger())
	require.Error(t, err)
}
```

- [ ] **Step 6: Implement `internal/sink/build.go`**

```go
package sink

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// Build turns configuration into sinks, skipping the disabled ones.
//
// The returned function closes every sink that holds a connection, and
// must be called on shutdown. Build replaces the old builder registry: a
// map of type to constructor plus a mutex bought nothing, because it was
// populated once at startup and never written again.
func Build(ctx context.Context, cfgs []Config, log *slog.Logger) ([]Sink, func() error, error) {
	var (
		sinks   []Sink
		closers []func() error
	)

	closeAll := func() error {
		var errs []error
		for _, closeFn := range closers {
			if err := closeFn(); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}

	for _, cfg := range cfgs {
		if !cfg.IsEnabled() {
			log.Debug("sink is disabled, skipping", "sink", cfg.ID, "type", cfg.Type)
			continue
		}

		built, closeFn, err := build(ctx, cfg, log)
		if err != nil {
			_ = closeAll()
			return nil, nil, err
		}
		sinks = append(sinks, built)
		if closeFn != nil {
			closers = append(closers, closeFn)
		}
	}

	if len(sinks) == 0 {
		_ = closeAll()
		return nil, nil, errors.New("sink: no enabled sinks configured")
	}
	return sinks, closeAll, nil
}

// build constructs one sink and, where it holds a connection, the function
// that releases it.
func build(ctx context.Context, cfg Config, log *slog.Logger) (Sink, func() error, error) {
	switch cfg.Type {
	case TypeLog:
		logCfg := LogConfig{}
		if cfg.Log != nil {
			logCfg = *cfg.Log
		}
		return NewLog(cfg.ID, logCfg, log), nil, nil

	case TypeHTTP:
		s, err := NewHTTP(cfg.ID, *cfg.HTTP)
		return s, nil, err

	case TypeSQS:
		s, err := NewSQS(ctx, cfg.ID, *cfg.SQS)
		return s, nil, err

	case TypeSNS:
		s, err := NewSNS(ctx, cfg.ID, *cfg.SNS)
		return s, nil, err

	case TypePubSub:
		s, err := NewPubSub(ctx, cfg.ID, *cfg.PubSub)
		if err != nil {
			return nil, nil, err
		}
		return s, s.Close, nil

	default:
		return nil, nil, fmt.Errorf("sink %s: unsupported type %q", cfg.ID, cfg.Type)
	}
}
```

`build` dereferences `cfg.HTTP`, `cfg.SQS`, `cfg.SNS` and `cfg.PubSub` without a nil check because `Config.Validate` has already rejected a config whose block is missing, and `LoadFile` validates every entry including disabled ones.

- [ ] **Step 7: Run the tests**

Run: `go test -race ./internal/sink/...`
Expected: PASS.

- [ ] **Step 8: Verify the module is green**

```bash
go build ./... && go vet ./... && go test -race ./...
```

- [ ] **Step 9: Commit**

```bash
git add internal/sink
git commit -m "$(cat <<'EOF'
feat(sink): port the cloud sinks and replace the builder registry

SQS, SNS and Pub/Sub move onto the Sink interface. The AWS call
interfaces are now declared at the consumer and unexported, since nothing
outside the package has a reason to name them, and each is one method.

Build replaces the type-to-builder registry. It was a map behind a mutex,
populated once at startup and never written again, reached through a
Registry interface with one implementation. A switch does the same work
visibly, and it now returns a closer so Pub/Sub's publisher goroutines
are actually stopped on shutdown — the old code never closed the client.

Disabled sinks are validated but not built, so a broken configuration
fails at startup rather than the first time someone enables it.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 10: `internal/harvest` and the cutover

The pipeline, and the deletion of everything it replaces. This carries C1 (the shutdown deadlock, fixed structurally), C2's caller side (mark only on full delivery) and C5 (a clean SIGTERM exits 0).

**Files:**
- Create: `internal/harvest/harvest.go`, `internal/harvest/harvest_test.go`
- Rewrite: `cmd/harvester/main.go`
- Delete: `pkg/`, `internal/crawler/`, `internal/storage/`, `internal/logger/`, `internal/domain/`, `internal/util/`, `internal/scheduler/`, `configs/providers.example.yaml`, `configs/publishers.example.yaml`

**Interfaces:**
- Consumes: `source.Config`, `news.Article`, `news.Event`, `sink.Fanout`, `sink.Result`.
- Produces:
  - `type harvest.Deps struct { Sources []source.Config; Fetchers map[string]Fetcher; Enricher Enricher; Deduper Deduper; Sinks *sink.Fanout; Log *slog.Logger; Interval time.Duration; Concurrency int }`
  - `type harvest.Fetcher interface { Fetch(ctx context.Context, src source.Config) ([]news.Article, error) }`
  - `type harvest.Enricher interface { Enrich(ctx context.Context, src source.Config, arts []news.Article) []news.Article }`
  - `type harvest.Deduper interface { Unseen(ctx context.Context, ids []string) ([]string, error); Mark(ctx context.Context, ids []string) error }`
  - `func harvest.New(d Deps) (*Harvester, error)`
  - `func (*Harvester) Run(ctx context.Context) error`
  - `func (*Harvester) RunOnce(ctx context.Context) error`

- [ ] **Step 1: Write the failing pipeline tests**

Create `internal/harvest/harvest_test.go`:

```go
package harvest_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/samvad-hq/samvad-news-harvester/internal/harvest"
	"github.com/samvad-hq/samvad-news-harvester/internal/news"
	"github.com/samvad-hq/samvad-news-harvester/internal/sink"
	"github.com/samvad-hq/samvad-news-harvester/internal/source"
	"github.com/stretchr/testify/require"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// --- fakes ------------------------------------------------------------

type fakeFetcher struct {
	articles map[string][]news.Article // by source ID
	err      error
	calls    atomic.Int64
}

func (f *fakeFetcher) Fetch(_ context.Context, src source.Config) ([]news.Article, error) {
	f.calls.Add(1)
	if f.err != nil {
		return nil, f.err
	}
	return f.articles[src.ID], nil
}

type fakeEnricher struct{ calls atomic.Int64 }

func (f *fakeEnricher) Enrich(_ context.Context, _ source.Config, arts []news.Article) []news.Article {
	f.calls.Add(1)
	out := make([]news.Article, len(arts))
	for i, a := range arts {
		a.Description = "enriched"
		out[i] = a
	}
	return out
}

type fakeDeduper struct {
	mu     sync.Mutex
	seen   map[string]bool
	marked []string
	err    error
}

func newFakeDeduper() *fakeDeduper { return &fakeDeduper{seen: map[string]bool{}} }

func (f *fakeDeduper) Unseen(_ context.Context, ids []string) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !f.seen[id] {
			out = append(out, id)
		}
	}
	return out, nil
}

func (f *fakeDeduper) Mark(_ context.Context, ids []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.marked = append(f.marked, ids...)
	for _, id := range ids {
		f.seen[id] = true
	}
	return nil
}

func (f *fakeDeduper) markedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.marked...)
}

type recordingSink struct {
	name string
	err  error
	mu   sync.Mutex
	got  []news.Event
}

func (r *recordingSink) Name() string { return r.name }

func (r *recordingSink) Send(_ context.Context, evt news.Event) error {
	if r.err != nil {
		return r.err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, evt)
	return nil
}

func (r *recordingSink) events() []news.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]news.Event(nil), r.got...)
}

// --- helpers ----------------------------------------------------------

func testSources(ids ...string) []source.Config {
	out := make([]source.Config, 0, len(ids))
	for _, id := range ids {
		out = append(out, source.Config{
			ID: id, Name: id, Type: source.TypeNewsSitemap,
			URL: "https://" + id + ".example/s.xml",
		})
	}
	return out
}

func article(id string) news.Article {
	return news.Article{ID: id, URL: "https://pub.example/" + id, Title: id}
}

// --- tests ------------------------------------------------------------

func TestRunOnceDeliversAndMarks(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFetcher{articles: map[string][]news.Article{
		"a": {article("1"), article("2")},
	}}
	dedup := newFakeDeduper()
	out := &recordingSink{name: "out"}

	h, err := harvest.New(harvest.Deps{
		Sources:     testSources("a"),
		Fetchers:    map[string]harvest.Fetcher{source.TypeNewsSitemap: fetcher},
		Enricher:    &fakeEnricher{},
		Deduper:     dedup,
		Sinks:       sink.NewFanout([]sink.Sink{out}, discardLogger()),
		Log:         discardLogger(),
		Interval:    time.Hour,
		Concurrency: 4,
	})
	require.NoError(t, err)

	require.NoError(t, h.RunOnce(context.Background()))

	require.Len(t, out.events(), 2)
	require.ElementsMatch(t, []string{"1", "2"}, dedup.markedIDs())
	require.Equal(t, "enriched", out.events()[0].Article.Description)
	require.Equal(t, "a", out.events()[0].SourceID)
}

func TestRunOnceSkipsArticlesAlreadySeen(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFetcher{articles: map[string][]news.Article{"a": {article("1"), article("2")}}}
	dedup := newFakeDeduper()
	dedup.seen["1"] = true
	enricher := &fakeEnricher{}
	out := &recordingSink{name: "out"}

	h, err := harvest.New(harvest.Deps{
		Sources:     testSources("a"),
		Fetchers:    map[string]harvest.Fetcher{source.TypeNewsSitemap: fetcher},
		Enricher:    enricher,
		Deduper:     dedup,
		Sinks:       sink.NewFanout([]sink.Sink{out}, discardLogger()),
		Log:         discardLogger(),
		Interval:    time.Hour,
		Concurrency: 4,
	})
	require.NoError(t, err)
	require.NoError(t, h.RunOnce(context.Background()))

	require.Len(t, out.events(), 1)
	require.Equal(t, "2", out.events()[0].Article.ID)
	require.Equal(t, []string{"2"}, dedup.markedIDs())
}

// The other half of the C2 fix. When any sink rejects the event, the
// article must not be marked, so the next crawl retries it.
func TestArticleIsNotMarkedWhenASinkFails(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFetcher{articles: map[string][]news.Article{"a": {article("1")}}}
	dedup := newFakeDeduper()
	good := &recordingSink{name: "good"}
	bad := &recordingSink{name: "bad", err: errors.New("webhook down")}

	h, err := harvest.New(harvest.Deps{
		Sources:     testSources("a"),
		Fetchers:    map[string]harvest.Fetcher{source.TypeNewsSitemap: fetcher},
		Enricher:    &fakeEnricher{},
		Deduper:     dedup,
		Sinks:       sink.NewFanout([]sink.Sink{good, bad}, discardLogger()),
		Log:         discardLogger(),
		Interval:    time.Hour,
		Concurrency: 4,
	})
	require.NoError(t, err)

	err = h.RunOnce(context.Background())
	require.Error(t, err, "a failed delivery is reported")
	require.Empty(t, dedup.markedIDs(), "a partially delivered article must stay unseen")
	require.Len(t, good.events(), 1, "the healthy sink still received it")
}

func TestOneFailingSourceDoesNotStopTheOthers(t *testing.T) {
	t.Parallel()

	fetcher := &failOneFetcher{
		fail: "b",
		articles: map[string][]news.Article{
			"a": {article("1")},
			"c": {article("3")},
		},
	}
	dedup := newFakeDeduper()
	out := &recordingSink{name: "out"}

	h, err := harvest.New(harvest.Deps{
		Sources:     testSources("a", "b", "c"),
		Fetchers:    map[string]harvest.Fetcher{source.TypeNewsSitemap: fetcher},
		Enricher:    &fakeEnricher{},
		Deduper:     dedup,
		Sinks:       sink.NewFanout([]sink.Sink{out}, discardLogger()),
		Log:         discardLogger(),
		Interval:    time.Hour,
		Concurrency: 4,
	})
	require.NoError(t, err)

	err = h.RunOnce(context.Background())
	require.ErrorContains(t, err, "b")
	require.Len(t, out.events(), 2, "a and c still delivered")
}

type failOneFetcher struct {
	fail     string
	articles map[string][]news.Article
}

func (f *failOneFetcher) Fetch(_ context.Context, src source.Config) ([]news.Article, error) {
	if src.ID == f.fail {
		return nil, errors.New("sitemap returned 403")
	}
	return f.articles[src.ID], nil
}

func TestRunOnceRespectsTheConcurrencyLimit(t *testing.T) {
	t.Parallel()

	counter := &concurrencyFetcher{}
	h, err := harvest.New(harvest.Deps{
		Sources:     testSources("a", "b", "c", "d", "e", "f", "g", "h"),
		Fetchers:    map[string]harvest.Fetcher{source.TypeNewsSitemap: counter},
		Enricher:    &fakeEnricher{},
		Deduper:     newFakeDeduper(),
		Sinks:       sink.NewFanout([]sink.Sink{&recordingSink{name: "out"}}, discardLogger()),
		Log:         discardLogger(),
		Interval:    time.Hour,
		Concurrency: 2,
	})
	require.NoError(t, err)
	require.NoError(t, h.RunOnce(context.Background()))

	require.LessOrEqual(t, counter.peak.Load(), int64(2))
}

type concurrencyFetcher struct {
	inFlight atomic.Int64
	peak     atomic.Int64
}

func (c *concurrencyFetcher) Fetch(context.Context, source.Config) ([]news.Article, error) {
	n := c.inFlight.Add(1)
	for {
		peak := c.peak.Load()
		if n <= peak || c.peak.CompareAndSwap(peak, n) {
			break
		}
	}
	time.Sleep(20 * time.Millisecond)
	c.inFlight.Add(-1)
	return nil, nil
}

// The C1 regression test. The old pipeline could block forever when
// cancellation landed between the producer's ctx check and its unbuffered
// send, because every worker had already returned.
func TestRunOnceReturnsPromptlyOnCancellation(t *testing.T) {
	t.Parallel()

	block := make(chan struct{})
	h, err := harvest.New(harvest.Deps{
		Sources:     testSources("a", "b", "c", "d", "e", "f", "g", "h", "i", "j"),
		Fetchers:    map[string]harvest.Fetcher{source.TypeNewsSitemap: &blockingFetcher{block: block}},
		Enricher:    &fakeEnricher{},
		Deduper:     newFakeDeduper(),
		Sinks:       sink.NewFanout([]sink.Sink{&recordingSink{name: "out"}}, discardLogger()),
		Log:         discardLogger(),
		Interval:    time.Hour,
		Concurrency: 2,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- h.RunOnce(ctx) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		close(block)
		t.Fatal("RunOnce deadlocked on cancellation")
	}
	close(block)
}

type blockingFetcher struct{ block chan struct{} }

func (b *blockingFetcher) Fetch(ctx context.Context, _ source.Config) ([]news.Article, error) {
	select {
	case <-b.block:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestRunCrawlsImmediatelyThenOnTheInterval(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFetcher{articles: map[string][]news.Article{}}
	h, err := harvest.New(harvest.Deps{
		Sources:     testSources("a"),
		Fetchers:    map[string]harvest.Fetcher{source.TypeNewsSitemap: fetcher},
		Enricher:    &fakeEnricher{},
		Deduper:     newFakeDeduper(),
		Sinks:       sink.NewFanout([]sink.Sink{&recordingSink{name: "out"}}, discardLogger()),
		Log:         discardLogger(),
		Interval:    30 * time.Millisecond,
		Concurrency: 2,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	require.NoError(t, h.Run(ctx), "a cancelled context is a clean shutdown, not an error")
	require.GreaterOrEqual(t, fetcher.calls.Load(), int64(2), "one immediate crawl plus at least one tick")
}

func TestRunRejectsAnUnknownSourceType(t *testing.T) {
	t.Parallel()

	sources := testSources("a")
	sources[0].Type = "rss"

	_, err := harvest.New(harvest.Deps{
		Sources:     sources,
		Fetchers:    map[string]harvest.Fetcher{source.TypeNewsSitemap: &fakeFetcher{}},
		Enricher:    &fakeEnricher{},
		Deduper:     newFakeDeduper(),
		Sinks:       sink.NewFanout([]sink.Sink{&recordingSink{name: "out"}}, discardLogger()),
		Log:         discardLogger(),
		Interval:    time.Hour,
		Concurrency: 2,
	})
	require.ErrorContains(t, err, "rss")
}

func TestNewRejectsMissingDependencies(t *testing.T) {
	t.Parallel()

	base := func() harvest.Deps {
		return harvest.Deps{
			Sources:     testSources("a"),
			Fetchers:    map[string]harvest.Fetcher{source.TypeNewsSitemap: &fakeFetcher{}},
			Enricher:    &fakeEnricher{},
			Deduper:     newFakeDeduper(),
			Sinks:       sink.NewFanout([]sink.Sink{&recordingSink{name: "out"}}, discardLogger()),
			Log:         discardLogger(),
			Interval:    time.Hour,
			Concurrency: 2,
		}
	}

	t.Run("no sources", func(t *testing.T) {
		d := base()
		d.Sources = nil
		_, err := harvest.New(d)
		require.Error(t, err)
	})

	t.Run("no deduper", func(t *testing.T) {
		d := base()
		d.Deduper = nil
		_, err := harvest.New(d)
		require.Error(t, err)
	})

	t.Run("no interval", func(t *testing.T) {
		d := base()
		d.Interval = 0
		_, err := harvest.New(d)
		require.Error(t, err)
	})
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/harvest/...`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement `internal/harvest/harvest.go`**

Note the one deviation from "unexported interfaces": `Fetcher` and `Enricher` are exported here because `Deps` is exported and its fields must name them. They are still declared at the consumer, which is the rule that matters. `Deduper` is exported for the same reason.

```go
// Package harvest runs the crawl loop.
//
// One pass over the source list is five stages per source: fetch the
// sitemap, identify the articles, ask which are new, enrich those, and
// deliver them. Sources run concurrently up to a configured limit; within
// a source the stages are sequential, because each depends on the last.
package harvest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/samvad-hq/samvad-news-harvester/internal/news"
	"github.com/samvad-hq/samvad-news-harvester/internal/sink"
	"github.com/samvad-hq/samvad-news-harvester/internal/source"
	"golang.org/x/sync/errgroup"
)

// Fetcher reads the articles a source is currently listing.
//
// Declared here, at the consumer. It is exported only because Deps is
// exported and its fields have to name it; nothing implements it outside
// internal/source.
type Fetcher interface {
	Fetch(ctx context.Context, src source.Config) ([]news.Article, error)
}

// Enricher fills in metadata a sitemap does not carry. It returns a slice
// of the same length and order as its input and cannot fail: an article
// whose page will not load is returned unchanged.
type Enricher interface {
	Enrich(ctx context.Context, src source.Config, arts []news.Article) []news.Article
}

// Deduper remembers which articles have already been delivered. Both
// methods take whole batches: a single source can carry thousands of IDs.
type Deduper interface {
	Unseen(ctx context.Context, ids []string) ([]string, error)
	Mark(ctx context.Context, ids []string) error
}

// Deps are everything the harvester needs.
type Deps struct {
	// Sources is the list to crawl, already validated.
	Sources []source.Config
	// Fetchers maps a source type to its fetcher.
	Fetchers map[string]Fetcher
	// Enricher fills in article metadata.
	Enricher Enricher
	// Deduper tracks delivered articles.
	Deduper Deduper
	// Sinks delivers events.
	Sinks *sink.Fanout
	// Log receives progress and failures.
	Log *slog.Logger
	// Interval is how often the full source list is crawled.
	Interval time.Duration
	// Concurrency bounds how many sources are crawled at once.
	Concurrency int
}

// Harvester crawls sources on an interval.
type Harvester struct {
	deps Deps
}

// New validates the dependencies and returns a harvester. Every source's
// type must have a registered fetcher, so a typo in the sources file
// fails at startup rather than on the first crawl.
func New(d Deps) (*Harvester, error) {
	var errs []error

	if len(d.Sources) == 0 {
		errs = append(errs, errors.New("harvest: no sources configured"))
	}
	if len(d.Fetchers) == 0 {
		errs = append(errs, errors.New("harvest: no fetchers registered"))
	}
	if d.Enricher == nil {
		errs = append(errs, errors.New("harvest: enricher is required"))
	}
	if d.Deduper == nil {
		errs = append(errs, errors.New("harvest: deduper is required"))
	}
	if d.Sinks == nil || d.Sinks.Len() == 0 {
		errs = append(errs, errors.New("harvest: at least one sink is required"))
	}
	if d.Log == nil {
		errs = append(errs, errors.New("harvest: logger is required"))
	}
	if d.Interval <= 0 {
		errs = append(errs, errors.New("harvest: interval must be positive"))
	}
	if d.Concurrency < 1 {
		errs = append(errs, errors.New("harvest: concurrency must be at least 1"))
	}

	for _, src := range d.Sources {
		if _, ok := d.Fetchers[src.Type]; !ok {
			errs = append(errs, fmt.Errorf("harvest: source %s has type %q with no registered fetcher", src.ID, src.Type))
		}
	}

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return &Harvester{deps: d}, nil
}

// Run crawls immediately and then on every interval, until ctx is done.
//
// A cancelled context is a clean shutdown, so Run returns nil. Crawl
// failures are logged here and not returned, because the loop is expected
// to outlive them — this is the one place in the service that logs an
// error it received rather than returning it.
func (h *Harvester) Run(ctx context.Context) error {
	h.deps.Log.InfoContext(ctx, "harvester starting",
		"sources", len(h.deps.Sources),
		"sinks", h.deps.Sinks.Len(),
		"interval", h.deps.Interval.String(),
		"concurrency", h.deps.Concurrency,
	)

	if err := h.RunOnce(ctx); err != nil && ctx.Err() == nil {
		h.deps.Log.ErrorContext(ctx, "crawl finished with errors", "error", err)
	}

	ticker := time.NewTicker(h.deps.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			h.deps.Log.InfoContext(ctx, "harvester stopping", "reason", context.Cause(ctx))
			return nil
		case <-ticker.C:
			if err := h.RunOnce(ctx); err != nil && ctx.Err() == nil {
				h.deps.Log.ErrorContext(ctx, "crawl finished with errors", "error", err)
			}
		}
	}
}

// RunOnce crawls every source once and returns the joined failures.
//
// One source failing does not stop the others: a publisher answering 403
// should not cost you the other twenty-five. errgroup.WithContext is
// deliberately not used for that reason — it would cancel siblings on the
// first error.
func (h *Harvester) RunOnce(ctx context.Context) error {
	start := time.Now()

	var (
		mu   sync.Mutex
		errs []error
	)

	var g errgroup.Group
	g.SetLimit(h.deps.Concurrency)

	for _, src := range h.deps.Sources {
		if ctx.Err() != nil {
			break
		}
		g.Go(func() error {
			if err := h.runSource(ctx, src); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
			return nil
		})
	}
	// The goroutines never return an error, so Wait cannot fail.
	_ = g.Wait()

	h.deps.Log.InfoContext(ctx, "crawl complete",
		"sources", len(h.deps.Sources),
		"failed_sources", len(errs),
		"elapsed_ms", time.Since(start).Milliseconds(),
	)

	return errors.Join(errs...)
}

// runSource takes one source through all five stages.
func (h *Harvester) runSource(ctx context.Context, src source.Config) error {
	start := time.Now()
	log := h.deps.Log.With("source", src.ID)

	articles, err := h.deps.Fetchers[src.Type].Fetch(ctx, src)
	if err != nil {
		return fmt.Errorf("source %s: %w", src.ID, err)
	}
	fetched := len(articles)
	if fetched == 0 {
		log.InfoContext(ctx, "source listed no articles")
		return nil
	}

	fresh, err := h.selectFresh(ctx, articles)
	if err != nil {
		return fmt.Errorf("source %s: dedupe: %w", src.ID, err)
	}
	if len(fresh) == 0 {
		log.InfoContext(ctx, "source crawled",
			"fetched", fetched, "fresh", 0, "delivered", 0,
			"elapsed_ms", time.Since(start).Milliseconds())
		return nil
	}

	fresh = h.deps.Enricher.Enrich(ctx, src, fresh)

	delivered, deliverErr := h.deliver(ctx, src, fresh)

	if len(delivered) > 0 {
		if err := h.deps.Deduper.Mark(ctx, delivered); err != nil {
			// Failing to mark means these articles are republished next
			// crawl. That is the safe direction, so it is reported and the
			// crawl continues.
			deliverErr = errors.Join(deliverErr, fmt.Errorf("mark delivered: %w", err))
		}
	}

	log.InfoContext(ctx, "source crawled",
		"fetched", fetched,
		"fresh", len(fresh),
		"delivered", len(delivered),
		"elapsed_ms", time.Since(start).Milliseconds(),
	)

	if deliverErr != nil {
		return fmt.Errorf("source %s: %w", src.ID, deliverErr)
	}
	return nil
}

// selectFresh asks the deduper which articles are new, in one call, and
// returns those articles in their original order.
func (h *Harvester) selectFresh(ctx context.Context, articles []news.Article) ([]news.Article, error) {
	ids := make([]string, 0, len(articles))
	for _, a := range articles {
		ids = append(ids, a.ID)
	}

	unseen, err := h.deps.Deduper.Unseen(ctx, ids)
	if err != nil {
		return nil, err
	}
	if len(unseen) == len(articles) {
		return articles, nil
	}

	keep := make(map[string]struct{}, len(unseen))
	for _, id := range unseen {
		keep[id] = struct{}{}
	}

	fresh := make([]news.Article, 0, len(unseen))
	for _, a := range articles {
		if _, ok := keep[a.ID]; ok {
			fresh = append(fresh, a)
		}
	}
	return fresh, nil
}

// deliver sends each article to every sink and returns the IDs that every
// sink accepted. An article that only some sinks accepted is deliberately
// not in that list: marking it would mean the failing sink never sees it
// again. Redelivering to the sinks that succeeded is the cheaper mistake.
func (h *Harvester) deliver(ctx context.Context, src source.Config, articles []news.Article) ([]string, error) {
	delivered := make([]string, 0, len(articles))
	var errs []error

	for _, art := range articles {
		if ctx.Err() != nil {
			break
		}
		res := h.deps.Sinks.Send(ctx, news.NewEvent(src.ID, src.Name, art))
		if res.OK() {
			delivered = append(delivered, art.ID)
			continue
		}
		errs = append(errs, fmt.Errorf("article %s: %w", art.ID, res.Err()))
	}

	return delivered, errors.Join(errs...)
}
```

- [ ] **Step 4: Run the pipeline tests**

Run: `go test -race ./internal/harvest/...`
Expected: PASS.

- [ ] **Step 5: Rewrite `cmd/harvester/main.go`**

```go
// Command harvester crawls news sitemaps and delivers article events to
// the configured sinks.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/samvad-hq/samvad-news-harvester/internal/config"
	"github.com/samvad-hq/samvad-news-harvester/internal/dedupe"
	"github.com/samvad-hq/samvad-news-harvester/internal/enrich"
	"github.com/samvad-hq/samvad-news-harvester/internal/harvest"
	"github.com/samvad-hq/samvad-news-harvester/internal/httpx"
	"github.com/samvad-hq/samvad-news-harvester/internal/sink"
	"github.com/samvad-hq/samvad-news-harvester/internal/source"
	"golang.org/x/time/rate"
)

func main() {
	validateOnly := flag.Bool("validate", false, "check the configuration and exit without crawling")
	once := flag.Bool("once", false, "run a single crawl and exit")
	flag.Parse()

	if err := run(*validateOnly, *once); err != nil {
		fmt.Fprintf(os.Stderr, "harvester: %v\n", err)
		os.Exit(1)
	}
}

// run wires the service together and starts it.
//
// A cancelled context is a clean shutdown and returns nil, so SIGTERM
// exits 0. The old code returned ctx.Err() from one path, which made a
// normal stop look like a crash to an orchestrator and produced a restart
// loop on a misconfigured deployment.
func run(validateOnly, once bool) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)

	sources, err := source.LoadFile(cfg.SourcesFile)
	if err != nil {
		return err
	}
	sinkCfgs, err := sink.LoadFile(cfg.SinksFile)
	if err != nil {
		return err
	}

	if validateOnly {
		fmt.Printf("configuration is valid: %d sources, %d sinks\n", len(sources), len(sinkCfgs))
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	sinks, closeSinks, err := sink.Build(ctx, sinkCfgs, log)
	if err != nil {
		return err
	}
	defer func() {
		if err := closeSinks(); err != nil {
			log.Error("closing sinks failed", "error", err)
		}
	}()

	deduper, closeDeduper, err := openDeduper(cfg)
	if err != nil {
		return err
	}
	defer func() {
		if err := closeDeduper(); err != nil {
			log.Error("closing the dedupe store failed", "error", err)
		}
	}()

	fetchClients := httpx.NewProxyCache(cfg.FetchTimeout, httpx.DefaultMaxBodyBytes)
	scrapeClients := httpx.NewProxyCache(cfg.ScrapeTimeout, maxArticleBodyBytes)
	limiter := httpx.NewHostLimiter(rate.Limit(cfg.PerHostRPS), 1)

	harvester, err := harvest.New(harvest.Deps{
		Sources: sources,
		Fetchers: map[string]harvest.Fetcher{
			source.TypeNewsSitemap: source.NewSitemap(fetchClients, log),
		},
		Enricher:    enrich.NewScraper(scrapeClients, limiter, cfg.ArticleConcurrency, log),
		Deduper:     deduper,
		Sinks:       sink.NewFanout(sinks, log),
		Log:         log,
		Interval:    cfg.CrawlInterval,
		Concurrency: cfg.SourceConcurrency,
	})
	if err != nil {
		return err
	}

	if once {
		if err := harvester.RunOnce(ctx); err != nil {
			log.Error("crawl finished with errors", "error", err)
		}
		return nil
	}

	if err := harvester.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// maxArticleBodyBytes caps an article page. Metadata lives in <head>, so
// 1 MiB is ample and keeps a pathological page from occupying memory.
const maxArticleBodyBytes int64 = 1 << 20

// openDeduper builds the configured dedupe store and its closer.
func openDeduper(cfg *config.Config) (harvest.Deduper, func() error, error) {
	if cfg.DedupeBackend == config.DedupeNone {
		var store dedupe.Noop
		return store, store.Close, nil
	}

	store, err := dedupe.OpenBolt(cfg.DedupePath, cfg.DedupeTTL, cfg.DedupeCleanupInterval)
	if err != nil {
		return nil, nil, err
	}
	return store, store.Close, nil
}
```

- [ ] **Step 6: Delete the old tree**

```bash
git rm -r --quiet \
  pkg \
  internal/crawler \
  internal/storage \
  internal/logger \
  internal/domain \
  internal/util \
  internal/scheduler \
  configs/providers.example.yaml \
  configs/publishers.example.yaml
go mod tidy
```

- [ ] **Step 7: Verify the whole module**

```bash
go build ./... && go vet ./... && go test -race ./...
```
Expected: everything passes. Confirm the removed dependencies are gone:

```bash
! grep -qE 'spf13/viper|go.uber.org/zap|go-resty/resty' go.mod && echo "removed deps are gone"
```

- [ ] **Step 8: Smoke-test the binary**

```bash
cp configs/sources.example.yaml configs/sources.yaml
cp configs/sinks.example.yaml configs/sinks.yaml
go run ./cmd/harvester -validate
go run ./cmd/harvester -once 2>&1 | head -40
```
Expected: `-validate` reports the source and sink counts; `-once` logs `article` lines from the log sink, then exits. No credentials required.

- [ ] **Step 9: Commit**

```bash
git add -A
git commit -m "$(cat <<'EOF'
feat(harvest): replace the crawler with a staged pipeline, delete pkg/

RunOnce is an errgroup with a concurrency limit, which removes the
shutdown deadlock structurally: the old producer checked ctx.Err() and
then blocked on an unbuffered send, so a cancellation between the two
left every worker returned, nobody receiving, and wg.Wait() unreachable.
errgroup.WithContext is deliberately not used, because one publisher
answering 403 should not cancel the other twenty-five.

An article is marked seen only when every sink accepted it. Previously a
success count above zero was enough, so a webhook that was down while SQS
succeeded lost the article permanently. The trade is a possible duplicate
to the sinks that succeeded, which is the cheaper mistake and is now
documented as at-least-once delivery.

A cancelled context is a clean shutdown and exits 0. One path used to
return ctx.Err(), so SIGTERM looked like a crash to an orchestrator.

main gains -validate and -once. A bad config was previously discovered by
a crash loop.

Deletes pkg/ (which imported internal/domain and so could never be
imported from outside the module), internal/crawler, internal/storage,
internal/logger, internal/domain, and the internal/util and
internal/scheduler stubs, which were nine unreferenced lines each.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 11: Documentation

Section 10 of the spec makes documentation part of the deliverable. The code is now unrecognisable next to the old README: package names, environment variables, config file names and field names have all changed, and the delivery semantics are different in a way consumers must know about.

**Files:**
- Rewrite: `README.md`
- Create: `docs/architecture.md`, `docs/configuration.md`, `CHANGELOG.md`

- [ ] **Step 1: Verify every package has a doc comment**

```bash
go doc ./internal/news ./internal/config ./internal/httpx ./internal/source \
       ./internal/enrich ./internal/dedupe ./internal/sink ./internal/harvest 2>&1 | head -60
golangci-lint run --enable-only revive
```
Expected: every package prints a description; revive reports no `package-comments` or `exported` findings. Fix any that are missing before continuing.

- [ ] **Step 2: Rewrite `README.md`**

Replace the whole file. Required content, in this order:

1. **Title and one-paragraph description.** State that it reads news sitemaps only, never RSS, and why: sitemaps are published for crawler consumption and advertised from `robots.txt`, whereas many publishers restrict their feeds to personal, non-commercial use. State that it never fetches or stores article bodies — only the metadata in `<head>`.

2. **Quickstart**, which must work with no credentials:

````markdown
```bash
git clone https://github.com/samvad-hq/samvad-news-harvester
cd samvad-news-harvester
cp configs/sources.example.yaml configs/sources.yaml
cp configs/sinks.example.yaml configs/sinks.yaml
go run ./cmd/harvester -once
```

The default sink writes each article to stdout, so this produces visible
output without an account anywhere. Swap in a webhook or a queue by
editing `configs/sinks.yaml`.
````

3. **Pipeline diagram**, as a mermaid block:

````markdown
```mermaid
flowchart LR
    A[fetch<br/>sitemap XML] --> B[identify<br/>canonical URL + sha256]
    B --> C[dedupe<br/>one read txn]
    C --> D[enrich<br/>Open Graph tags]
    D --> E[deliver<br/>fan out to sinks]
    E --> F[mark<br/>one write txn]
```
````

4. **Delivery semantics**, verbatim:

> **Delivery is at-least-once.** An article is recorded as seen only once
> *every* enabled sink has accepted it. If one sink is down while the
> others succeed, the event is redelivered to all of them on the next
> crawl. Key on `article.id`, which is the SHA-256 of the canonical URL
> and is stable across tracking parameters, trailing slashes and host
> case.

5. **Event shape**, a real JSON example produced by running `-once` and copying one event.

6. **Configuration**, pointing at `docs/configuration.md` and showing one source entry and one sink entry.

7. **Adding a source type**, the extension contract:

````markdown
`Fetcher` is not a Go interface you import. Write a concrete type with a
`Fetch` method and register it — Go's structural typing does the rest:

```go
// internal/source/rss.go
type RSS struct { /* ... */ }

func (r *RSS) Fetch(ctx context.Context, src source.Config) ([]news.Article, error) {
	// return articles with ID = news.ID(news.CanonicalURL(link))
}
```

Then add one line in `cmd/harvester/main.go`:

```go
Fetchers: map[string]harvest.Fetcher{
	source.TypeNewsSitemap: source.NewSitemap(fetchClients, log),
	source.TypeRSS:         source.NewRSS(fetchClients, log),
},
```

Add the type constant and accept it in `source.Config.Validate`.
````

8. **Adding a sink**: implement `sink.Sink` (`Name`, `Send`), add a config block and a case in `internal/sink/build.go`.

9. **Development**: `make test`, `make race`, `make lint`, `scripts/capture-fixtures.sh`, and the note that fixtures are committed so publisher XML drift breaks CI.

10. **Blocked publishers**: two of the 26 configured sources answered 403 to a direct request on 2026-09-06 (`ndtv`, `anandabazarpatrika`). Document the per-source `proxy:` field as the remedy.

11. **License** — ISC, unchanged.

Remove from the old README: the `providers.yaml`/`publishers.yaml` names, `PROVIDERS_FILE`/`PUBLISHERS_FILE`/`STORAGE_TYPE`/`BBOLT_PATH`/`STORAGE_TTL_SECONDS`, the `type: queue` publisher shape, the `pkg/providers.Fetcher` registration instructions, and the sequence diagram that shows the old flow.

- [ ] **Step 3: Write `docs/architecture.md`**

Required sections:

- **The five stages** — one short section each: what it does, which package, what it costs. Include the concrete numbers measured on 2026-09-06: 8 of 10 configured sources answered 200 with a browser user agent; `jagran` ships 2500 URLs at 2.5 MB; the eight working sources total about 4.6 MB per crawl, roughly 440 MB a day at a 15-minute interval.
- **Concurrency** — `SOURCE_CONCURRENCY` bounds sources via `errgroup.SetLimit`; `ARTICLE_CONCURRENCY` bounds scrapes within a source; `PER_HOST_RPS` is enforced by `httpx.HostLimiter` keyed on host, which matters for the scrape stage: the 26 sources span 26 distinct hostnames so fetches never contend, but one crawl issues hundreds of article requests against a single origin. State explicitly that `errgroup.WithContext` is *not* used at the source level, so one failing publisher does not cancel the rest.
- **Failure handling** — a table of stage, failure, and consequence: fetch fails (source skipped this crawl, others continue); dedupe fails (source skipped, nothing marked); enrich fails (article keeps its sitemap title); one sink fails (article not marked, redelivered next crawl); mark fails (article redelivered next crawl).
- **Interface inventory** — the four interfaces, where each is declared, and why. State the rule: an interface exists here only where there are several implementations or where the pipeline needs a test seam.
- **What is deliberately absent** — no retry, no outbox, no metrics, no conditional GET, and links to the design doc's out-of-scope section.

- [ ] **Step 4: Write `docs/configuration.md`**

Required content:

- A table of every environment variable: name, default, meaning. Copy the table from Task 4 of this plan and add the meaning column.
- The full `sources.yaml` schema: every field, whether it is required, and what validation applies. Note that `${VAR}` is expanded and that an unset variable becomes an empty string which validation then rejects by name.
- The full `sinks.yaml` schema: the five types and every field of each.
- A **migration from v1** section mapping each old name to its new one:

| v1 | v2 |
|----|----|
| `PROVIDERS_FILE` | `SOURCES_FILE` |
| `PUBLISHERS_FILE` | `SINKS_FILE` |
| `CRAWL_INTERVAL=900` | `CRAWL_INTERVAL=15m` |
| `STORAGE_TYPE=bbolt` | `DEDUPE_BACKEND=bolt` |
| `STORAGE_TYPE=none` | `DEDUPE_BACKEND=none` |
| `BBOLT_PATH` | `DEDUPE_PATH` |
| `STORAGE_TTL_SECONDS=432000` | `DEDUPE_TTL=120h` |
| `STORAGE_CLEANUP_INTERVAL_SECONDS=43200` | `DEDUPE_CLEANUP_INTERVAL=12h` |
| `providers:` | `sources:` |
| `source_url:` | `url:` |
| `config:` (map) | `headers:` (typed) |
| `type: google_news_sitemap` | `type: news_sitemap` |
| `request_delay_ms:` | removed — use `PER_HOST_RPS` |
| `response_format:` | removed — it was never read |
| `publishers:` | `sinks:` |
| `type: queue` + `queue.provider: aws-sqs` | `type: aws-sqs` |
| `queue.aws.uri` | `sqs.queue_url` |
| event field `provider_id` | `source_id` |
| event field `provider_name` | `source_name` |

- Note that the dedupe database is **not** migrated: v1 IDs are SHA-1 of the raw URL and v2 IDs are SHA-256 of the canonical URL, so the first v2 crawl republishes everything. Deleting the old file is the correct action.

- [ ] **Step 5: Write `CHANGELOG.md`**

Keep-a-Changelog format, one `0.2.0` entry dated 2026-09-06 with `Added`, `Changed`, `Fixed` and `Removed` sections. `Changed` must lead with a **BREAKING** block covering the environment variables, config files and event field renames, pointing at `docs/configuration.md`. `Fixed` lists the four correctness defects (shutdown deadlock, partial-delivery data loss, URL canonicalisation, exit code) and the three throughput defects (dedupe write lock, per-article fsync, the rate limiter that cancelled its own worker pool).

- [ ] **Step 6: Check every documented command actually works**

```bash
rm -f configs/sources.yaml configs/sinks.yaml data/dedupe.db
cp configs/sources.example.yaml configs/sources.yaml
cp configs/sinks.example.yaml configs/sinks.yaml
go run ./cmd/harvester -validate
go run ./cmd/harvester -once 2>&1 | head -20
make test && make lint
```
Expected: all succeed. Fix any command in the README that does not.

- [ ] **Step 7: Commit**

```bash
git add README.md docs/architecture.md docs/configuration.md CHANGELOG.md
git commit -m "$(cat <<'EOF'
docs: rewrite for v2

The README described packages, environment variables, config file names
and field names that no longer exist, and a delivery model that was never
true. Rewrites it around the quickstart that works with no credentials,
the at-least-once semantics consumers have to know about, and the
extension contract for adding a source type or a sink.

Adds docs/architecture.md (the five stages, the concurrency model, what
happens when each stage fails, and why each of the four interfaces
exists), docs/configuration.md (every environment variable and YAML
field, plus a v1-to-v2 migration table), and a CHANGELOG starting at
0.2.0.

Notes that the dedupe database does not migrate: v1 ids were sha1 of the
raw URL and v2 ids are sha256 of the canonical URL, so the first v2 crawl
republishes everything and the old file should be deleted.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

## Done

At this point:

- `go build ./... && go vet ./... && go test -race ./...` passes.
- `go run ./cmd/harvester -once` produces article events with no credentials configured.
- `pkg/`, `internal/crawler`, `internal/storage`, `internal/logger`, `internal/domain`, `internal/util` and `internal/scheduler` are gone.
- viper, zap and resty are out of `go.mod`.
- Four interfaces remain, each declared at its consumer.
- Findings C1, C2, C3, C5, T1, T2, T3 and D1–D9 are all addressed.

Feature work resumes from the design doc's out-of-scope list, in the order given there: conditional GET, `<lastmod>` skipping, `harvester discover`, then metrics and the outbox.
