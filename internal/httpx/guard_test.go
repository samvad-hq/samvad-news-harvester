package httpx

// In-package so the table below can call isPrivateNetworkAddress
// directly and pin the full set of blocked and allowed ranges without
// going through a real dial or request.

import (
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsPrivateNetworkAddress(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		ip      string
		blocked bool
	}{
		{"IPv4 loopback", "127.0.0.1", true},
		{"IPv6 loopback", "::1", true},
		{"IPv4 link-local unicast", "169.254.1.1", true},
		{"IPv4 link-local multicast", "224.0.0.1", true},
		{"IPv6 link-local unicast", "fe80::1", true},
		{"RFC1918 10/8", "10.0.0.1", true},
		{"RFC1918 172.16/12", "172.16.5.1", true},
		{"RFC1918 192.168/16", "192.168.1.1", true},
		{"IPv6 unique-local", "fd00::1", true},
		{"unspecified IPv4", "0.0.0.0", true},
		{"unspecified IPv6", "::", true},
		{"carrier-grade NAT 100.64.0.0/10 start", "100.64.0.1", true},
		{"carrier-grade NAT 100.64.0.0/10 end", "100.127.255.254", true},
		{"general IPv4 multicast", "239.1.2.3", true},
		{"IPv6 multicast", "ff02::1", true},
		{"limited broadcast", "255.255.255.255", true},
		{"cloud metadata address", "169.254.169.254", true},

		{"public IPv4", "8.8.8.8", false},
		{"public IPv4, different range", "93.184.216.34", false},
		{"public IPv6", "2606:4700:4700::1111", false},
		{"just outside carrier-grade NAT range", "100.63.255.255", false},
		{"just past carrier-grade NAT range", "100.128.0.0", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ip := net.ParseIP(tt.ip)
			require.NotNil(t, ip, "test fixture %q must parse", tt.ip)
			require.Equal(t, tt.blocked, isPrivateNetworkAddress(ip), tt.ip)
		})
	}
}
