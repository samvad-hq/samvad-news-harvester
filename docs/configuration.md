# Configuration

The harvester is configured from three places: environment variables (an
optional `configs/.env` file, then real environment variables which always
win), `configs/sources.yaml` (what to crawl), and `configs/sinks.yaml`
(where to deliver). All configuration is loaded and validated once at
startup — `internal/config.Load`, `source.LoadFile`, `sink.LoadFile` — so a
misconfigured deployment fails immediately with every problem reported at
once, rather than one restart per mistake.

## Environment variables

| Variable | Default | Meaning |
|---|---|---|
| `APP_NAME` | `samvad-news-harvester` | Process label carried in logs only. |
| `APP_ENV` | `development` | Environment label carried in logs only. |
| `LOG_LEVEL` | `info` | Minimum level written to stdout: `debug`, `info`, `warn` or `error`. |
| `SOURCES_FILE` | `./configs/sources.yaml` | Path to the sources file. YAML (`.yaml`/`.yml`) or JSON (`.json`), chosen by extension. |
| `SINKS_FILE` | `./configs/sinks.yaml` | Path to the sinks file. Same format rule as above. |
| `CRAWL_INTERVAL` | `15m` | How often the full source list is crawled, as a Go duration (`90s`, `15m`, `2h`). Ignored by `-once`. |
| `SOURCE_CONCURRENCY` | `8` | How many sources are crawled at once. |
| `ARTICLE_CONCURRENCY` | `10` | How many article metadata fetches run at once, per source. |
| `DELIVERY_CONCURRENCY` | `8` | How many of one source's articles are delivered to the sinks at once. Set it to `1` for a sink that cannot take parallel writes or is aggressively rate-limited. |
| `PER_HOST_RPS` | `2` | Requests per second allowed to any one hostname, shared across every source and article fetch to that host. |
| `DEDUPE_BACKEND` | `bolt` | `bolt` to persist delivered article IDs to disk, or `none` to disable deduplication (every crawl republishes everything). |
| `DEDUPE_PATH` | `./data/dedupe.db` | The bbolt file. Required (and validated) only when `DEDUPE_BACKEND=bolt`. |
| `DEDUPE_TTL` | `120h` | How long a delivered article ID is remembered before it is eligible to be forgotten and, if seen again, redelivered. |
| `DEDUPE_CLEANUP_INTERVAL` | `12h` | How often expired IDs are swept from the store. |
| `FETCH_TIMEOUT` | `30s` | Per-request timeout for one sitemap fetch. |
| `SCRAPE_TIMEOUT` | `15s` | Per-request timeout for one article metadata fetch. |

Duration variables must parse with Go's `time.ParseDuration` (e.g. `90s`,
`15m`, `2h`); integer variables must parse with base-10 `strconv.Atoi`;
`PER_HOST_RPS` must parse as a float. An unparsable value is reported by
name and the default is used for that field only, so one typo does not
mask every other problem. `SOURCES_FILE` and `SINKS_FILE` are the one
exception: an explicitly-set-but-empty value (`SOURCES_FILE=` in the
environment) is preserved as empty rather than falling back to the
default, so that `Validate` can reject it by name instead of silently
using the default file.

## URL canonicalisation

Every article's stable ID (`article.id` in the event JSON) is the SHA-256
of a canonical form of its URL, produced by `news.CanonicalURL`
(`internal/news/url.go`) before hashing. Canonicalisation:

1. Lowercases the scheme and the host.
2. Drops the port when it is the default for the scheme (`:443` on
   `https`, `:80` on `http`).
3. Drops the fragment (everything after `#`) entirely.
4. Removes every query parameter whose name begins with `utm_`, matched
   case-insensitively, plus this fixed, also case-insensitive list:
   `fbclid`, `gclid`, `gbraid`, `wbraid`, `msclkid`, `igshid`, `mc_cid`,
   `mc_eid`, `ref`, `ref_src`, `_ga`, `yclid`, `twclid`.
5. Sorts whatever query parameters remain, so parameter order cannot
   change an article's identity.
6. Trims a trailing slash from a non-root path; the root path is
   normalised to `/`.

`ref` and `ref_src` are a deliberate judgement call, not an oversight:
both are overwhelmingly used for referral tracking rather than to select
different content, and dropping them matches common tracking-parameter
blocklists.

This is what makes `article.id` identical across the differences a link
picks up when it is shared, retweeted, or pasted with a tracking tag — see
[the README](../README.md#delivery-is-at-least-once) for why consumers
must key on it.

## `sources.yaml` schema

```yaml
sources:
  - id: thehindu                # required, unique, becomes source_id on every event
    name: The Hindu             # required, the publisher's display name
    type: news_sitemap          # required, must be exactly "news_sitemap" — the only type
    url: https://www.thehindu.com/sitemap/googlenews/all/all.xml   # required, absolute http(s) URL
    proxy: ${PROXY_URL}         # optional, absolute http(s) URL; routes only this source's requests
    headers:
      user_agent: "..."         # required, non-empty — several publishers 403 an unidentified crawler
      accept: "..."             # optional
      accept_language: "..."    # optional
      cache_control: "..."      # optional
```

| Field | Required | Validation |
|---|---|---|
| `id` | yes | Non-empty after trimming; must be unique within the file. |
| `name` | yes | Non-empty after trimming. |
| `type` | yes | Must equal `news_sitemap` (`source.TypeNewsSitemap`) — the only supported source type today. |
| `url` | yes | Must parse as an absolute URL with an `http` or `https` scheme and a host. |
| `proxy` | no | When present, same URL validation as `url`. `${VAR}` is expanded first (see below), so an unset `PROXY_URL` fails validation by name rather than silently sending requests direct. |
| `headers.user_agent` | yes | Non-empty after trimming. |
| `headers.accept`, `headers.accept_language`, `headers.cache_control` | no | Sent as-is when present; omitted from the request when blank. |

A validation error naming `url` or `proxy` never echoes a userinfo
component (`user:pass@host`) even when the offending value carried one —
`proxy` is a normal place to put basic-auth credentials, and the error
strips them before formatting, or omits the value entirely when it failed
to parse at all.

**Fetching `url` and any `<loc>` it points to is guarded against private
networks by default.** `internal/httpx` refuses to dial a loopback,
link-local, unique-local, RFC1918-private, or unspecified address —
checked on every resolved address the transport actually dials, including
one reached only via a redirect — because a sitemap's contents (and every
article URL read from it) come from third-party XML a publisher controls.
Setting `proxy` on a source disables the guard for that source's requests,
since the dial in that case targets the proxy's own address (operator
configuration) rather than the ultimate destination; a proxy on a private
address is expected and keeps working. There is no per-source way to
disable the guard without a proxy — it is not configuration, by design.

`headers` is a typed struct (`source.Headers`), not a free-form map — a
misspelled key such as `usre_agent` fails to compile against the schema
rather than silently sending no header at all.

## `sinks.yaml` schema

```yaml
sinks:
  - id: local-log        # required, unique
    type: log             # one of: log, http, aws-sqs, aws-sns, gcp-pubsub
    enabled: true          # optional, defaults to true when omitted
    log: { ... }           # the block matching `type`
```

Every entry is validated at load time regardless of `enabled`, so a broken
block fails at startup rather than the first time someone flips it on.

### `type: log`

| Field | Required | Notes |
|---|---|---|
| `log.level` | no | `debug`, `info`, `warn` or `error`. Defaults to `info`; an unrecognised value also falls back to `info`. |

Needs no credentials and no network. This is the sink the example config
enables, so a fresh clone produces visible output on the first run.

### `type: http`

| Field | Required | Notes |
|---|---|---|
| `http.url` | yes | Absolute `http` or `https` URL. Must not expand to an empty string. |
| `http.method` | no | Defaults to `POST`; upper-cased. |
| `http.timeout` | no | Go duration; defaults to `10s`; must not be negative. |
| `http.headers` | no | Map of header name to value; entries where either side is blank (typically an unset `${VAR}`) are dropped rather than sent empty. |

Delivers the event as a JSON POST body with `Content-Type:
application/json`. Any response outside 2xx is treated as a delivery
failure.

**A transport failure never logs the full webhook URL.** For Slack,
Discord and Microsoft Teams incoming webhooks the URL path *is* the
bearer credential, so `internal/sink.HTTP` logs and returns only a
redacted form — scheme and host, with the path replaced by a marker and
any userinfo or query string dropped — never the URL used to actually
make the request. A non-2xx response body is still quoted (bounded to 512
bytes), since that comes from the far end, not from the URL itself.

### `type: aws-sqs`

| Field | Required |
|---|---|
| `sqs.queue_url` | yes |
| `sqs.region` | yes |
| `sqs.access_key_id` | yes |
| `sqs.secret_access_key` | yes |

All four must be non-empty after `${VAR}` expansion. Delivers the event
JSON as the message body with `source_id` as a string message attribute.

### `type: aws-sns`

| Field | Required |
|---|---|
| `sns.topic_arn` | yes |
| `sns.region` | yes |
| `sns.access_key_id` | yes |
| `sns.secret_access_key` | yes |

Same shape as SQS, publishing to a topic instead of a queue.

### `type: gcp-pubsub`

| Field | Required |
|---|---|
| `pubsub.project_id` | yes |
| `pubsub.topic` | yes |
| `pubsub.credentials_file` | no — falls back to Application Default Credentials when blank |

Publishes the event JSON as the message data with `source_id` as an
attribute, and blocks until the client library confirms the publish or the
context ends.

### Enabling a cloud sink

The four non-default sinks (`http`, `aws-sqs`, `aws-sns`, `gcp-pubsub`)
ship **commented out** in `configs/sinks.example.yaml`, not present-but-
`enabled: false`. Entries are validated even when disabled, so a
config block sitting there with unset `${VAR}`s would fail validation at
startup the moment it stopped being a comment. To use one: uncomment its
block, set the environment variables it references, and set `enabled:
true` (or remove the `enabled` line, since the default is on).

### `${VAR}` expansion

Both files go through the same expansion step (`internal/config.DecodeFile`)
before being parsed as YAML or JSON: every `${VAR}` reference — the braced
form only, never a bare `$VAR` — is replaced with the value of that
environment variable. An unset variable expands to an empty string, which
then fails whatever validation applies to that field, naming the field in
the error. This is the mechanism that keeps credentials out of the
committed config files while still failing loudly when one is missing,
rather than sending a request with a blank header or an empty queue URL.

## Migration from v1

The dedupe database is **not** migrated, and this is the one step that
must be done manually. v1 article IDs were the SHA-1 of the raw,
uncanonicalised URL; v2 IDs are the SHA-256 of the canonical URL (see
[the README](../README.md#delivery-is-at-least-once) for what
canonicalisation does). The two ID spaces share nothing, so a v1 dedupe
file is not merely stale against v2 — it is comparing apples to a hash
function that no longer runs. The first v2 crawl against an existing v1
`bbolt` file will therefore republish every article as if it had never
been seen, and there is no way to avoid that short of deleting the file:

```bash
rm -f data/dedupe.db   # or wherever BBOLT_PATH pointed
```

Every other setting has a direct v1-to-v2 rename:

| v1 | v2 |
|----|----|
| `PROVIDERS_FILE` | `SOURCES_FILE` |
| `PUBLISHERS_FILE` | `SINKS_FILE` |
| `CRAWL_INTERVAL=900` (seconds) | `CRAWL_INTERVAL=15m` (duration string) |
| `STORAGE_TYPE=bbolt` | `DEDUPE_BACKEND=bolt` |
| `STORAGE_TYPE=none` | `DEDUPE_BACKEND=none` |
| `BBOLT_PATH` | `DEDUPE_PATH` |
| `STORAGE_TTL_SECONDS=432000` | `DEDUPE_TTL=120h` |
| `STORAGE_CLEANUP_INTERVAL_SECONDS=43200` | `DEDUPE_CLEANUP_INTERVAL=12h` |
| `providers:` (top-level YAML key) | `sources:` |
| `source_url:` (per-entry field) | `url:` |
| `config:` (untyped map of headers) | `headers:` (typed struct) |
| `type: google_news_sitemap` | `type: news_sitemap` |
| `request_delay_ms:` | removed — use `PER_HOST_RPS` instead |
| `response_format:` | removed — it was never read by the old code |
| `publishers:` (top-level YAML key) | `sinks:` |
| `type: queue` with `queue.provider: aws-sqs` | `type: aws-sqs` (flat, no nested `queue` block) |
| `queue.aws.uri` | `sqs.queue_url` |
| event field `provider_id` | `source_id` |
| event field `provider_name` | `source_name` |

A v1 consumer reading events off a queue must be updated for the last two
renames before pointing it at a v2 deployment, or it will silently read
`""` for both fields.
