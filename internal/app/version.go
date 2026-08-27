package app

import (
	"runtime/debug"
	"strings"
)

// sourceVersion is what a build that is not a release calls itself.
//
// There is no "dev". A release is whatever CI cuts at a tag, and everything
// else is somebody's working copy -- there is no third kind of build, and a
// word that implies one invites a binary to describe itself as unfinished when
// it is the thing running in production.
//
// Deliberately not a number. internal/updates refuses to order a version it
// cannot parse and reports nothing as behind rather than inventing a
// comparison, so a build with no release behind it is quiet rather than wrong.
const sourceVersion = "source"

// Version is what this build calls itself, and the only place the answer
// lives.
//
// Set at link time with -X, which is how a release is stamped: CI builds at a
// tag and passes it to the image, the binaries and the packages alike, then
// checks the result reports it before publishing anything. A build from a
// working tree is stamped from the release manifest committed to the source.
//
// It used to be possible to name a version in the environment beside a
// deployment, which compose passed to the build. That is a second answer to a
// question the source already answers, and it was the one that went stale: a
// host ran for weeks reporting the release it was built after rather than the
// code it was running. There is no environment variable now, on purpose.
var Version = sourceVersion

// Whatever the linker was told, enriched with what the build itself recorded
// when it was told nothing. Assigned once, before anything reads it -- both
// `mcpd -version` and the dashboard read this variable rather than calling a
// function, and a version that changed depending on when it was asked would be
// worse than either answer.
func init() {
	info, ok := debug.ReadBuildInfo()
	Version = resolveVersion(Version, info, ok)
}

// resolveVersion decides what an unstamped build should call itself.
//
// Separated from init so it can be tested against build information a test
// constructs, rather than only against whatever built the test binary.
func resolveVersion(stamped string, info *debug.BuildInfo, ok bool) string {
	// A builder that named a version is believed. It knows something this
	// cannot work out -- which tag it built at -- and second-guessing it would
	// make a release report something other than its own number.
	if v := strings.TrimSpace(stamped); v != "" && v != sourceVersion {
		return v
	}
	if !ok || info == nil {
		return sourceVersion
	}

	// `go install github.com/spoked/mcpd/cmd/mcpd@v0.3.0` records the module
	// version, and that is the one case where an unstamped binary knows its
	// release exactly.
	//
	// Only a clean tag is taken. A build from a checkout gets a *pseudo*
	// version -- v0.2.1-0.20260827011106-d1be8e8934ed -- which names the
	// release *after* the last tag and a release that does not exist. Anything
	// ordering versions truncates at the first hyphen, so trusting it would
	// have a working copy claim to be 0.2.1, and go quiet about a real 0.2.1
	// when one was published. A hyphen is what tells the two apart.
	if v := info.Main.Version; v != "" && v != "(devel)" && !strings.Contains(v, "-") {
		return v
	}

	// Otherwise the commit, which Go records when it builds inside a checkout.
	// It says nothing about which release this is near, and is not meant to:
	// it answers the question somebody actually has in front of a build that
	// is behaving oddly, which is "which code is this".
	var revision string
	var modified bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if revision == "" {
		return sourceVersion
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	// After a +, so that anything ordering versions sees "source" and declines
	// to order it. A build from a working copy must not be reported as behind
	// a release, nor as level with one.
	out := sourceVersion + "+" + revision
	if modified {
		out += ".modified"
	}
	return out
}
