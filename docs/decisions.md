# Decisions

The calls that shaped this service, and the reasoning behind each. They
are recorded because the reasoning is the part that does not survive in
the code: a reader can see *what* the service does from
[architecture.md](architecture.md), but not which alternatives were
weighed, or what would have to change for a decision to be worth
revisiting.

Each entry names what would make it wrong. A decision no evidence could
overturn is a preference, and preferences do not need a document.

---

## Sitemaps only, never RSS or Atom

**Decision.** The harvester reads news sitemaps. It does not read feeds,
and adding feed support is not planned.

**Why.** This is a licensing decision, not a technical one. News sitemaps
exist to be read by crawlers and are advertised from `robots.txt` — a
publisher who ships one is inviting automated consumption. Many of the
same publishers restrict their RSS and Atom feeds to personal,
non-commercial use in their terms of service. Feeds would be easier to
parse and cheaper to poll, and that is not the question.

**Revisit when.** A publisher's terms permit feed consumption for the use
in hand, or the deployment is genuinely personal and non-commercial. The
answer is per publisher, not global.

---

## Delivery is at-least-once

**Decision.** An article is recorded as seen only once *every* enabled
sink has accepted it. A partial failure redelivers the event to all sinks
on the next crawl, including the ones that already received it.

**Why.** The alternative that was in place before — marking an article
delivered when *any* sink accepted it — silently and permanently lost the
article for every sink that was down at that moment. Not on the next
crawl; ever. A duplicate is a cost the consumer can absorb by keying on
`article.id`, which is stable across redeliveries. A missing article is
not recoverable by anyone.

**What it costs.** Consumers must deduplicate. This is stated in the
README rather than left implicit, because a consumer that assumes
exactly-once will be wrong in production and not in testing.

**Revisit when.** An outbox exists — per-sink delivery state, so a replay
goes only to the sinks that actually missed the event. That removes the
duplicate without reintroducing the loss, and is the reason at-least-once
is a default rather than a destination.

---

## Article identity is the SHA-256 of a canonical URL

**Decision.** `article.id` is `sha256(CanonicalURL(link))`. Canonicalisation
lowercases the scheme and host, drops the default port and the fragment,
strips tracking parameters, sorts the rest, and trims a trailing slash.
The canonical URL is kept on the article, not just hashed and thrown away.

**Why.** Identity is what deduplication, delivery and consumer-side
idempotency all agree on, so it has to be stable across the differences a
link picks up in the wild. Hashing the raw URL — the previous behaviour —
meant a `utm_source`, a trailing slash or a differently-cased host each
produced a second ID for one article, and dedupe missed precisely the case
it exists to catch. Keeping the canonical URL on the article lets a
consumer that deduplicates downstream apply the same normalisation instead
of inventing its own.

**What it costs.** IDs are not comparable with v1's, so the dedupe store
does not migrate. Stripping `ref` and `ref_src` is a judgement call: both
are overwhelmingly referral tracking, but a publisher could in principle
use one to select content, and that URL would collapse into its
unparameterised sibling.

**Revisit when.** A configured publisher is found using a stripped
parameter to select content rather than to track a referral.

---

## Interfaces are discovered, not designed

**Decision.** An interface exists only where there are several real
implementations, where a consumer needs a seam to substitute a fake in a
test, or where a shared algorithm needs a generic constraint. Everything
else is a concrete type with exported methods. Interfaces are declared at
their consumer, hold one or two methods, and constructors return concrete
types. There are seven in the service; the inventory and the reason for
each is in [architecture.md](architecture.md#interface-inventory).

**Why.** The previous codebase had four files named `interfaces.go`, and
the name was the symptom: Go places an interface next to the code that
consumes it, so a file collecting them guarantees they sit away from their
consumers. Six of those interfaces had exactly one implementation, so they
were indirection with no payoff — and constructors returning interfaces
hid every other method of the concrete type from callers.

**What it costs.** Adding a source type means editing the repository
rather than implementing an exported contract from outside it. Go requires
recompilation for that anyway, so nothing is actually lost.

**Revisit when.** A second implementation of something concrete appears
for a real reason. That is what happened to the dedupe store, which now
has three.

---

## SSRF is guarded in two layers, and one of them is advisory

**Decision.** The HTTP client refuses private, loopback, link-local,
CGNAT, multicast, broadcast and unspecified destinations. A dial-level
hook checks every address the transport actually dials, including after a
redirect. A URL-level check runs before the request is issued.

**Why.** Sitemap `<loc>` children and article URLs are third-party XML a
publisher controls, and the adoption story is strangers adding their own
sources. Without a guard, a hostile or compromised publisher could point
the harvester at `169.254.169.254` or at a service on the operator's
network and have this process fetch it on their behalf.

The second layer exists because the first is blind for a proxied source:
every dial the hook sees targets the proxy's own address, never the
request's real destination, and an HTTPS request tunnels through that same
connection via CONNECT. Before the URL-level check, a proxied source had
no SSRF protection at all.

**What it costs, stated plainly.** For a proxied client the URL-level
check is advisory. Resolution happens twice — once here, once inside the
proxy — and nothing guarantees they agree. A publisher racing a DNS change
between the two can pass this check with a public address while the proxy
connects to a private one. The guard closes the common case and cannot
close that window. A self-hoster pointing the harvester at an internal
mirror must opt in explicitly.

**Revisit when.** The proxy path can be made to resolve once and dial a
checked address, which would need cooperation from the proxy protocol
rather than from this code.

---

## `-once` exits non-zero only when every source failed

**Decision.** A partial failure exits 0. Only a crawl in which every
configured source failed exits non-zero. Errors are logged either way;
only the exit code is conditional.

**Why.** Three of the 26 configured sources answer 403 by design and need
a proxy that may not be configured. Exiting non-zero on any failure would
give a cron wrapper a permanently red exit code, and an operator learns to
ignore one of those within a week. A total failure — nothing fetched,
nothing delivered — is the case actually worth waking someone for.

**Revisit when.** Per-source health is tracked well enough to distinguish
"this source has failed for six hours" from "this source failed once", at
which point the exit code stops being the only signal available.

---

## A proxy is configured per source, not per process

**Decision.** `proxy` is a field on a source, not an environment variable.

**Why.** Only some publishers refuse a direct request, and routing every
request through a third party costs money, adds latency, and hands that
third party the full crawl. Per-source keeps the blast radius to the
sources that need it.

**What it does not fix.** A proxy addresses IP reputation, which is
usually what is being refused. It does not disguise the TLS handshake:
`net/http` sends Go's default ClientHello, whose JA3/JA4 signature does
not resemble a browser's, and a publisher fingerprinting at that layer can
still refuse a request from a clean address. Changing that would need a
client that builds its own ClientHello, which this service does not do.

**Revisit when.** A configured publisher is confirmed to be blocking on
fingerprint rather than address, and the crawl matters enough to take on a
non-standard TLS stack.

---

## Redis is for durability, not for scale

**Decision.** `DEDUPE_BACKEND=redis` stores delivered IDs in Redis instead
of a local bbolt file. Running more than one harvester process is not a
supported configuration.

**Why.** Container platforms with an ephemeral filesystem lose the bbolt
file on every redeploy, and the next crawl then republishes every article.
That is the problem Redis solves here.

**What it deliberately does not do.** Delivery is marked after the fact,
not claimed before it, so two processes sharing one Redis can both read an
article as unseen and both deliver it. Preventing that needs a claim at
check time, which trades duplicates for losing an article outright if a
process dies between claiming and delivering — a different set of
semantics that should be chosen deliberately, not inherited by anyone who
happens to point two instances at one store.

**Revisit when.** Horizontal scale is actually needed, at which point the
claim-based path is a feature with its own design, not a configuration
change.

---

## No retry, no outbox, no metrics — yet

**Decision.** A failed fetch, scrape or delivery is not retried within a
crawl. There is no per-sink delivery state and no metrics endpoint.

**Why.** The next scheduled crawl is the retry, and for a service polling
sitemaps every fifteen minutes that is usually soon enough. Each of these
is real work with its own design, and shipping them half-done inside a
rewrite would have made the rewrite unreviewable.

**Revisit when.** Now, in order: conditional GET and `<lastmod>` skipping
first, since `httpx.Response` already carries the validators; then
metrics; then the outbox.
