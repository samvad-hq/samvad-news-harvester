# taja-khobor

Taja Khobor is an open-source Go microservice that periodically collects news links, enriches them with basic metadata, and emits lightweight events downstream. It is built to stay small, composable, and friendly to new contributors.

## What it does (current state)
- Pluggable providers: each source implements the `pkg/providers.Fetcher` interface and is wired through a registry. Currently supported Google News sitemaps:
  - NDTV
  - Times of India
  - The Hindu
  - Financial Express
  - Anandabazar Patrika
  - Ei Samay
  - Aaj Tak
  - Dainik Jagran
  - Dinamalar
  - Daily Thanthi
- Config-driven crawling: providers are declared in YAML/JSON with per-provider headers (User-Agent, Accept, etc.) and `request_delay_ms` throttling.
- Link extraction: provider fetchers pull URLs from sitemaps/RSS. Common helpers handle Google News sitemap parsing and article ID generation.
- Metadata enrichment: fetched links are optionally enriched from OG/title/description/image tags with goquery; cancellation returns whatever was processed so far.
- Shared HTTP client abstraction (resty under the hood) and centralized header builder.
- Structured logging with zap; publisher/storage layers remain stubs for contributors to extend.

## Quickstart
Prereqs: Go 1.22+.

```bash
cp configs/providers.yaml configs/providers.local.yaml  # tweak locally if needed
go run ./cmd/collector
```

Environment defaults (overridable via env vars):
- `PROVIDERS_FILE` (default `./configs/providers.yaml`)
- `PUBLISHERS_FILE` (default `./configs/publishers.yaml`)
- `LOG_LEVEL` (default `info`)
- `CRAWL_INTERVAL` (default `15m`)

## Configuring providers
Providers live in `configs/providers.yaml` (YAML or JSON is accepted). Example:

```yaml
providers:
  - id: ndtv
    name: NDTV News
    type: https
    source_url: https://www.ndtv.com/sitemap/google-news-sitemap
    response_format: xml
    request_delay_ms: 500
    config:
      user_agent: <required>   # always set this; headers are never defaulted
      accept: <optional>
      accept_language: <optional>
      cache_control: <optional>
```

Adding a provider:
1. Implement `pkg/providers.Fetcher` in a new file (keep provider-specific logic isolated).
2. Register it in `pkg/providers.DefaultFetcherRegistry`.
3. Add the provider entry to `configs/providers.yaml`.

## Project layout
```
cmd/collector/          # entrypoint
internal/config         # viper/env config loader
internal/crawler        # orchestrates provider fetchers + enrichment
internal/logger         # zap setup and helpers
pkg/httpclient          # shared HTTP client interfaces + resty adapter
pkg/providers           # provider registry, fetcher interfaces, provider impls, sitemap helpers
pkg/publisher           # outbound publisher stub
configs/                # provider and app config examples
```

## Contributing
- Open to PRs and issues; keep changes small and focused.
- Prefer one file per provider, registered via the fetcher registry to preserve pluggability.
- Run `gofmt` and `go test ./...` before submitting.
- Discussions and improvements around storage/publishing/backoff are welcome—those layers are intentionally minimal today.
