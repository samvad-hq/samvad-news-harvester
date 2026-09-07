# Security Policy

## Reporting a vulnerability

Please report security issues privately, through GitHub's
[private vulnerability reporting](https://github.com/samvad-hq/samvad-news-harvester/security/advisories/new)
rather than as a public issue.

Include what you need to make the problem reproducible: the version or
commit, the configuration that triggers it, and what you observed. If it
involves a specific publisher's sitemap or a specific sink, say which —
the interesting cases in this service usually depend on the shape of
untrusted input.

Expect an acknowledgement within a week. This is a small project with no
paid staff and no bug bounty; what it can offer is a straight answer and
credit in the changelog if you want it.

Secret scanning with push protection is enabled on this repository, so a
webhook URL or cloud key committed by accident is caught before it
reaches the remote. That is a backstop, not a substitute for keeping
credentials in the environment and referencing them as `${VAR}` in the
configuration files.

## Supported versions

The most recent release only. Older tags do not receive fixes.

## What this service treats as untrusted

Two things, and the distinction matters when judging whether something is
a vulnerability:

**Publisher input is untrusted.** Sitemap XML, the `<loc>` URLs inside it,
sitemap index children, and every article page fetched during enrichment
all come from third parties who can change them at will. The service is
designed on the assumption that a publisher may be hostile or compromised.

**Operator configuration is trusted.** `configs/sources.yaml`,
`configs/sinks.yaml`, the environment, and any `proxy` URL are supplied by
whoever runs the service. A sink URL pointing at a private address, or a
proxy on a private network, is a legitimate configuration rather than an
attack.

## Protections in place

- **SSRF.** The HTTP client refuses to dial loopback, link-local,
  unique-local, RFC1918-private, carrier-grade-NAT, multicast, broadcast
  and unspecified addresses. The check runs on every address the transport
  actually dials, so a redirect gets no free pass, and again at the URL
  level before a request is issued. A sitemap index child on an unrelated
  host is skipped rather than fetched.
- **Credential redaction.** Webhook URLs are redacted to scheme and host
  in every error and log line, because for Slack, Discord and Teams the
  URL path is the bearer credential. Proxy and Redis URLs are never echoed
  in errors. Cloud credentials are reported as missing by name, never with
  their value.
- **Bounded reads.** Response bodies are capped at read time rather than
  after buffering, a sitemap index is bounded in depth and child count, and
  a single sitemap is capped at 5000 URLs.
- **No code execution from input.** The service parses XML and HTML and
  reads `<head>` metadata. It does not execute JavaScript, does not follow
  arbitrary schemes, and never writes fetched content to disk.

## Known limits

Stated because a limit you know about is safer than one you assume away:

- **The SSRF guard is advisory for a proxied source.** When a source sets
  `proxy`, the dial-level check sees only the proxy's address, so a
  URL-level check stands in. That check resolves the hostname
  independently of the proxy, and a publisher racing a DNS change between
  the two lookups can pass it while the proxy connects somewhere private.
  This cannot be closed from inside this codebase.
- **Delivery is at-least-once**, so a sink may receive the same event more
  than once. This is a correctness contract, not a defect.
- **The dedupe store is not authenticated by this service.** Whatever
  guards your Redis instance or the filesystem holding the bbolt file is
  what guards the dedupe state.
