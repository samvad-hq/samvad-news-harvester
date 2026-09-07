package main

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
)

// version is the release this binary was built from. It is empty in an
// ordinary `go build`, where buildVersion falls back to the VCS revision
// the toolchain stamps in. A release build sets it explicitly:
//
//	go build -ldflags "-X main.version=v0.2.0" ./cmd/harvester
var version string

// buildVersion reports what is running, in one line.
//
// An operator looking at a misbehaving deployment needs to know which
// build it is before anything else, and "the tag we think we deployed" is
// not evidence. The toolchain records the commit and whether the tree was
// dirty, so this reads that rather than trusting a constant someone
// remembered to bump.
func buildVersion() string {
	name := version
	revision, modified, buildTime := "", false, ""

	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				revision = setting.Value
			case "vcs.time":
				buildTime = setting.Value
			case "vcs.modified":
				modified = setting.Value == "true"
			}
		}
	}

	// info.Main.Version is deliberately not used as a fallback. For a
	// binary built from a checkout it holds a pseudo-version derived from
	// the nearest tag — "v0.1.1-0.2026...-c2d2606" against a v0.1.0 tag —
	// which names a release that does not exist. "devel" plus the commit
	// is less informative and more true.
	if name == "" {
		name = "devel"
	}

	var b strings.Builder
	b.WriteString("samvad-news-harvester ")
	b.WriteString(name)

	if revision != "" {
		// Short form: the full hash is rarely what anyone reads out loud.
		if len(revision) > 12 {
			revision = revision[:12]
		}
		fmt.Fprintf(&b, " (%s", revision)
		if modified {
			// Worth shouting: a dirty build does not correspond to any
			// commit, so bisecting against it is meaningless.
			b.WriteString("-dirty")
		}
		b.WriteString(")")
	}
	if buildTime != "" {
		fmt.Fprintf(&b, " built %s", buildTime)
	}
	fmt.Fprintf(&b, " %s %s/%s", runtime.Version(), runtime.GOOS, runtime.GOARCH)

	return b.String()
}
