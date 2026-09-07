package httpx

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"syscall"
)

// cgnatBlock is 100.64.0.0/10, the carrier-grade NAT range (RFC 6598).
// net.IP.IsPrivate only covers RFC 1918 and the IPv6 unique-local block,
// so this range needs an explicit check.
var cgnatBlock = mustParseCIDR("100.64.0.0/10")

// limitedBroadcast is 255.255.255.255, the IPv4 limited-broadcast
// address. No net.IP predicate covers it, so it is compared explicitly.
var limitedBroadcast = net.IPv4(255, 255, 255, 255)

func mustParseCIDR(s string) *net.IPNet {
	_, block, err := net.ParseCIDR(s)
	if err != nil {
		// s is a compile-time constant; a parse failure here is a bug in
		// this file, not a runtime condition.
		panic("httpx: invalid CIDR literal " + s)
	}
	return block
}

// guardedDialer returns the dialer New should use for cfg.
//
// The guard is skipped in two cases: when the caller opted out via
// WithPrivateNetworksAllowed, and when a proxy is configured. The proxy
// case matters because of how net/http actually dials: when Transport.Proxy
// is set, every dial this Control func sees targets the proxy's own
// address, never the ultimate destination (for an HTTPS request the
// destination is reached by tunnelling a CONNECT through that same
// connection, which this hook never sees). A proxy address is operator
// configuration, not something a hostile publisher's XML can influence, so
// it must keep working even when it happens to be on a private network —
// rejecting it here would just break every proxied source.
func guardedDialer(cfg clientConfig) *net.Dialer {
	d := &net.Dialer{}
	if cfg.allowPrivate || cfg.proxy != nil {
		return d
	}
	d.Control = rejectPrivateAddresses
	return d
}

// rejectPrivateAddresses is a net.Dialer.Control func. Control runs after
// the address has been resolved but before the connection is actually
// dialed, which is what makes this effective against a redirect: net/http
// follows a redirect by dialing a new connection through the same
// Transport, so this hook fires again for the new address with no extra
// wiring needed. Checking the resolved IP rather than the original
// hostname also closes a DNS-rebinding gap that a hostname-string check
// would miss.
func rejectPrivateAddresses(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("httpx: parse dial address %q: %w", address, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("httpx: refusing to dial unresolved address %q", address)
	}
	if isPrivateNetworkAddress(ip) {
		return fmt.Errorf("httpx: refusing to dial private network address %s", ip)
	}
	return nil
}

// isPrivateNetworkAddress reports whether ip is loopback, link-local
// (unicast or multicast), a unique-local or RFC1918 private address,
// carrier-grade NAT space, multicast, the limited-broadcast address, or
// unspecified — the ranges a sitemap or article URL has no legitimate
// reason to point at, and that an operator's own internal services are
// likely to be listening on.
func isPrivateNetworkAddress(ip net.IP) bool {
	return ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsPrivate() ||
		ip.IsUnspecified() ||
		// 100.64.0.0/10, carrier-grade NAT (RFC 6598): shared address
		// space an ISP routes internally, not covered by IsPrivate.
		cgnatBlock.Contains(ip) ||
		// General multicast: IsLinkLocalMulticast above only covers
		// 224.0.0.0/24 and its IPv6 equivalent; IsMulticast covers the
		// rest of 224.0.0.0/4 and all of IPv6 multicast.
		ip.IsMulticast() ||
		// 255.255.255.255, the IPv4 limited-broadcast address. No
		// stdlib predicate covers it, so it is compared explicitly.
		ip.Equal(limitedBroadcast)
}

// guardURL checks rawURL's host before a request is issued, closing a gap
// the dial-level hook (guardedDialer, rejectPrivateAddresses) cannot
// cover: when a client is built with a proxy, every dial the Control func
// sees targets the proxy's own address, never the request's destination,
// so nothing at the dial layer ever inspects it. This check runs
// regardless of whether a proxy is configured. For a direct client it
// duplicates the dialer hook, which is fine — it just fails one step
// sooner, and it keeps the guard to a single code path to reason about.
//
// The two layers are not equally strong. For a direct client this check
// resolves the host the same way the dialer eventually will, so it
// blocks on the same result. For a proxied client it is only advisory:
// resolution happens twice — once here, once inside the proxy — and
// nothing guarantees they agree. A hostname can legitimately resolve
// differently between the two lookups (a load balancer, split-horizon
// DNS), and a hostile publisher racing a DNS change between them (a
// rebind) can pass this check with a public address and still have the
// proxy connect the request to a private one. This guard cannot see or
// control what the proxy resolves, so that window stays open for a
// proxied client; it still blocks the common case of a URL that names a
// private address or a hostname that only ever resolves to one.
func guardURL(ctx context.Context, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("httpx: parse url %q: %w", rawURL, err)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("httpx: url %q has no host", rawURL)
	}

	if ip := net.ParseIP(host); ip != nil {
		if isPrivateNetworkAddress(ip) {
			return fmt.Errorf("httpx: refusing to request private network address %s", ip)
		}
		return nil
	}

	// A resolution failure here isn't this guard's concern — the normal
	// request path will hit the same failure and report it.
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil //nolint:nilerr
	}
	for _, addr := range addrs {
		if !isPrivateNetworkAddress(addr.IP) {
			return nil
		}
	}
	return fmt.Errorf("httpx: refusing to request %q, which resolves only to private network addresses", host)
}
