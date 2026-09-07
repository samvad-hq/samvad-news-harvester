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
	"fbclid":  true,
	"gclid":   true,
	"gbraid":  true,
	"wbraid":  true,
	"msclkid": true,
	"igshid":  true,
	"mc_cid":  true,
	"mc_eid":  true,
	"ref":     true,
	"ref_src": true,
	"_ga":     true,
	"yclid":   true,
	"twclid":  true,
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

	// Check for missing scheme or host first, before checking scheme value,
	// so that relative paths wrap ErrNotAbsoluteURL consistently.
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("%w: %q", ErrNotAbsoluteURL, raw)
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("news: unsupported scheme %q", u.Scheme)
	}

	u.Scheme = scheme
	u.Host = canonicalHost(u.Host, scheme)
	u.Fragment = ""
	u.RawFragment = ""
	u.RawQuery = canonicalQuery(u.Query())
	canonicalPath(u)

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

// canonicalPath removes trailing slashes from a path and normalises the
// root to "/", operating on the escaped form so that a percent-encoded
// separator such as %2F stays inside its segment instead of being
// re-escaped into a real one.
func canonicalPath(u *url.URL) {
	escaped := u.EscapedPath()
	trimmed := strings.TrimRight(escaped, "/")
	if trimmed == "" {
		trimmed = "/"
	}
	if trimmed == escaped {
		return
	}
	unescaped, err := url.PathUnescape(trimmed)
	if err != nil {
		unescaped = trimmed
	}
	u.Path = unescaped
	u.RawPath = trimmed
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
