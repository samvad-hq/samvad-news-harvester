# Contributing

Contributions are welcome. This file covers what you need to build the
project and the conventions the code holds to; the reasoning behind the
design lives in [docs/decisions.md](docs/decisions.md).

## Getting set up

You need **Go 1.25** — `golang.org/x/net` requires it at the versions this
module pins — and **golangci-lint v2**:

```bash
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2
```

`make lint` checks the major version and tells you this if you have v1.

```bash
cp configs/sources.example.yaml configs/sources.yaml
cp configs/sinks.example.yaml configs/sinks.yaml
go run ./cmd/harvester -once
```

That crawls the 26 configured publishers and prints every article to
stdout. No account, no credentials, no cloud service.

Optionally, `make hooks` points `core.hooksPath` at `scripts/githooks`,
which runs `goimports` and `gofmt` over staged Go files before each
commit.

## Before you open a pull request

```bash
make test    # go test ./...
make race    # go test -race ./...
make lint    # go vet + golangci-lint
```

CI runs all three plus a `gofmt -l` check and `govulncheck`. All must pass.

## Conventions

These are enforced by review, and mostly by the linter:

- **Go 1.25.** Use `any`, never `interface{}`. Use range-over-int.
- **Interfaces are declared at their consumer**, hold one or two methods,
  and are unexported unless the implementations are chosen at runtime.
  There are seven in the whole service and each has a stated reason — see
  the [interface inventory](docs/architecture.md#interface-inventory).
  Adding an eighth needs an argument, not just a use.
- **Constructors return concrete types**, never interfaces.
- **Errors are returned or logged, never both.** Only `harvest.Run` logs
  an error it received.
- **Error strings are lowercase and unpunctuated.**
- **Never put a sink, proxy or Redis URL in an error or a log line.** Each
  can carry credentials — for a Slack, Discord or Teams webhook the URL
  path *is* the bearer token. Redaction helpers exist; reuse them rather
  than writing a third one.
- **Every package and every exported identifier carries a doc comment.**
  The linter enforces this, and the v2 exclusion presets that would
  disable it are deliberately switched off.
- **Documentation is part of the change, not a follow-up.** A pull request
  that changes behaviour updates the README, `docs/architecture.md` or
  `docs/configuration.md` in the same commit.

## Tests

- Table-driven, with `testify/require`.
- `httptest.Server` for anything that fetches; `miniredis` for the Redis
  store. **No test talks to the network.**
- A test that cannot fail is not a test. If you add one for a concurrency
  property, check that it fails against the sequential version first.

Sitemap fixtures under `internal/source/testdata/` are committed on
purpose: publisher XML drifts, and a shape change should break CI rather
than production. Re-record them with `scripts/capture-fixtures.sh` when
adding a source or when a live parse starts failing.

## Adding a source type or a sink

Both are short and are documented in the README:
[adding a source type](README.md#adding-a-source-type),
[adding a sink](README.md#adding-a-sink). Neither needs a registry
updated or an interface imported.

## Reporting a security issue

Privately, not as an issue — see [SECURITY.md](SECURITY.md).
