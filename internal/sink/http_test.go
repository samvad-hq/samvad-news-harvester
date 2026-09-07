package sink

// This file is in-package (not sink_test) so it can exercise the
// unexported redactURL helper directly, without needing a real request to
// observe its output.

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedactURLDropsUserinfoQueryAndReplacesThePath(t *testing.T) {
	t.Parallel()

	got := redactURL("https://user:pass@hooks.slack.com/services/T00/B00/xxxxx?foo=bar#frag")
	require.Equal(t, "https://hooks.slack.com/…", got)
}

func TestRedactURLLeavesAHostWithNoPathReadable(t *testing.T) {
	t.Parallel()

	require.Equal(t, "https://example.com", redactURL("https://example.com"))
	require.Equal(t, "https://example.com/", redactURL("https://example.com/"))
}

func TestRedactURLHandlesAnUnparseableURL(t *testing.T) {
	t.Parallel()

	require.Equal(t, "<unparseable url>", redactURL("://not a url"))
}
