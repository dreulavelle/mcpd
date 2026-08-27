package app

import (
	"runtime/debug"
	"testing"
)

func buildInfo(mainVersion string, settings ...debug.BuildSetting) *debug.BuildInfo {
	return &debug.BuildInfo{
		Main:     debug.Module{Path: "github.com/spoked/mcpd", Version: mainVersion},
		Settings: settings,
	}
}

func setting(key, value string) debug.BuildSetting {
	return debug.BuildSetting{Key: key, Value: value}
}

func TestResolveVersion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		stamped string
		info    *debug.BuildInfo
		ok      bool
		want    string
	}{
		{
			// What CI does at a tag. A builder that named a version knows
			// something this cannot work out, so it is believed.
			"a stamped release", "0.3.0", buildInfo("(devel)"), true, "0.3.0",
		},
		{
			// What the image does when nobody passes a version: the release
			// manifest, marked as a build from a working tree.
			"a stamped source build", "0.2.0+source", nil, false, "0.2.0+source",
		},
		{
			"go install at a tag", "source", buildInfo("v0.3.0"), true, "v0.3.0",
		},
		{
			// A checkout build gets a pseudo-version naming the release after
			// the last tag -- one that does not exist. Taking it would have a
			// working copy claim to be 0.2.1 and go quiet about a real 0.2.1.
			"a pseudo-version is refused", "source",
			buildInfo("v0.2.1-0.20260827011106-d1be8e8934ed",
				setting("vcs.revision", "d1be8e8934ed09ae41d517dfe31cb7a4ee8e0521")),
			true, "source+d1be8e8934ed",
		},
		{
			"a clean checkout", "source",
			buildInfo("(devel)",
				setting("vcs.revision", "d1be8e8934ed09ae41d517dfe31cb7a4ee8e0521"),
				setting("vcs.modified", "false")),
			true, "source+d1be8e8934ed",
		},
		{
			"a modified checkout", "source",
			buildInfo("(devel)",
				setting("vcs.revision", "d1be8e8934ed09ae41d517dfe31cb7a4ee8e0521"),
				setting("vcs.modified", "true")),
			true, "source+d1be8e8934ed.modified",
		},
		{
			// No git at build time, which is every build inside the image.
			"nothing recorded", "source", buildInfo("(devel)"), true, "source",
		},
		{"no build info at all", "source", nil, false, "source"},
		{"an empty stamp", "", nil, false, "source"},
		{"a whitespace stamp", "   ", nil, false, "source"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveVersion(tc.stamped, tc.info, tc.ok); got != tc.want {
				t.Errorf("resolveVersion = %q, want %q", got, tc.want)
			}
		})
	}
}
