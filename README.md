# taja-khobor

Taja Khobor is a tiny Go service that crawls news sources, enriches links with lightweight metadata, and fans events out to queues or webhooks. It is intentionally small, pluggable, and friendly to new contributors.

## What it does
- Crawls providers registered in YAML/JSON; today ships with Google News sitemap fetcher. Tested with dozens of Indian news sources.
- Enriches links with titles/descriptions/images via goquery (best effort, returns partial results on cancel).
- Fans out JSON events to multiple publishers (HTTP webhooks or queues: AWS SQS/SNS, GCP Pub/Sub) via a registry.
- Optional dedupe layer (BoltDB file by default) so previously published article IDs are skipped.

## Prereqs
- Go 1.24+
- Access to any sinks you enable (webhook URL, SQS/SNS creds, Pub/Sub topic, etc.).

## Quickstart (local)
1) Providers: edit `configs/providers.yaml` (or point `PROVIDERS_FILE` to your own). Set a real `user_agent`; headers are not defaulted.  
2) Publishers: copy `configs/publishers.example.yaml` to `configs/publishers.yaml`, then **disable sinks you don’t own** (`enabled: false`) or set the required env vars for the ones you keep. The default example enables GCP Pub/Sub, so toggle it off if you don’t have creds handy.  
3) Run the collector:
```bash
go run ./cmd/collector
```
The process stays alive and triggers a crawl every `CRAWL_INTERVAL` (default 15m).

## Providers
Providers live in `configs/providers.yaml` (YAML or JSON). Google News example:
```yaml
providers:
  - id: ndtv
    name: NDTV News
    type: google_news_sitemap
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
1. For another Google News sitemap: add an entry with `type: google_news_sitemap` and set headers.  
2. For new source types: implement `pkg/providers.Fetcher`, register it in `pkg/providers.DefaultFetcherRegistry`, and use its type (or provider id override) in config.

## Publishers
Publishers live in `configs/publishers.yaml` (YAML or JSON). There are two `type` values:
- `queue` with `queue.provider`: `aws-sqs`, `aws-sns`, or `gcp`
- `http` for webhooks

Example covering all kinds:
```yaml
publishers:
  - id: aws-sqs-queue
    type: queue
    enabled: true
    queue:
      provider: aws-sqs
      aws:
        uri: "${AWS_SQS_QUEUE_URL}"
        region: "${AWS_SQS_REGION}"
        access_key_id: "${AWS_SQS_ACCESS_KEY_ID}"
        secret_access_key: "${AWS_SQS_SECRET_ACCESS_KEY}"
```

Unknown or disabled types are ignored at runtime. Use env expansion in YAML for secrets; never commit real credentials.

## Storage / deduplication
Default storage uses BoltDB (`STORAGE_TYPE=bbolt`, `BBOLT_PATH=./data/cache.db`) to remember published article IDs and skip re-sending. Set `STORAGE_TYPE=none` to disable dedupe. Control retention via `STORAGE_TTL_SECONDS` and cleanup cadence via `STORAGE_CLEANUP_INTERVAL_SECONDS`.

## Development
- Run `go test ./...` before sending changes (tests are welcome).
- Keep changes small and focused; one provider per file and register via the fetcher registry to preserve pluggability.
- Logging uses zap; publishers and providers accept injected clients for easier testing/mocking.
- Enable the bundled pre-commit hook (runs gofmt on staged Go files): `make hooks`

## Contributing & sharing
Issues and PRs are very welcome, especially around:
- New providers (RSS/sitemaps), backoff/retry helpers, scheduler wiring, and non-Bolt storage options.
- Additional publishers (Azure Service Bus, Kafka, etc.).

Feel free to open an issue before a PR if you want to give or get quick feedback on scope or approach.
