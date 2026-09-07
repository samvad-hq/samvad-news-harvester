package source

// In-package so it can exercise sameRegistrableHost and registrableSuffix
// directly. Proving the loose comparison end to end through Fetch would
// need a sitemap index child on a real, differently-named host reachable
// in the test — not achievable with httptest, which only ever binds to
// loopback — so the heuristic itself is unit-tested here instead.

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSameRegistrableHostAllowsAPlainSubdomain(t *testing.T) {
	t.Parallel()

	require.True(t, sameRegistrableHost("thehindu.com", "cdn.thehindu.com"))
	require.True(t, sameRegistrableHost("www.thehindu.com", "thehindu.com"))
}

func TestSameRegistrableHostHandlesAShortSecondLevelLabel(t *testing.T) {
	t.Parallel()

	// A short label ahead of a two/three-letter TLD (co.in, co.uk, com.au)
	// is treated as part of the suffix, so a CDN subdomain still matches.
	require.True(t, sameRegistrableHost("thehindu.co.in", "assets.thehindu.co.in"))
	require.True(t, sameRegistrableHost("bbc.co.uk", "static.bbc.co.uk"))
}

func TestSameRegistrableHostRejectsAnUnrelatedDomain(t *testing.T) {
	t.Parallel()

	require.False(t, sameRegistrableHost("thehindu.com", "attacker.example"))
	require.False(t, sameRegistrableHost("thehindu.co.in", "notthehindu.co.in"))
}
