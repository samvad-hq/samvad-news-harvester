package main

import (
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildVersionNamesTheBinaryAndTheToolchain(t *testing.T) {
	got := buildVersion()

	require.True(t, strings.HasPrefix(got, "samvad-news-harvester "), got)
	require.Contains(t, got, runtime.Version())
	require.Contains(t, got, runtime.GOOS+"/"+runtime.GOARCH)
}

// An unstamped build must say "devel" rather than inventing a version.
// The pseudo-version the toolchain derives from the nearest tag would
// claim a release that was never cut.
func TestBuildVersionSaysDevelWhenUnstamped(t *testing.T) {
	original := version
	t.Cleanup(func() { version = original })

	version = ""
	require.Contains(t, buildVersion(), "devel")
}

func TestBuildVersionUsesTheInjectedVersion(t *testing.T) {
	original := version
	t.Cleanup(func() { version = original })

	version = "v9.9.9"
	got := buildVersion()
	require.Contains(t, got, "v9.9.9")
	require.NotContains(t, got, "devel")
}
