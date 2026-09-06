package main

import (
	"testing"
	"time"
)

// The container image is alpine with ca-certificates and nothing else, so there
// is no /usr/share/zoneinfo for time.LoadLocation to read -- and the root
// filesystem is read-only, so installing one is not an option either.
//
// Without the blank `time/tzdata` import in main.go, every zone but UTC fails
// to load here. A backup schedule set to 04:00 America/Chicago would then run
// at 04:00 UTC: right for half the year, an hour wrong for the other half, on a
// host nobody is watching.
//
// This test is in package main because that is where the import is. It passes
// on a developer's machine either way -- there is a zoneinfo directory there --
// so it is a guard against the import being removed as unused rather than a
// reproduction of the container.
func TestTheTimezoneDatabaseIsCompiledIn(t *testing.T) {
	for _, name := range []string{
		"America/Chicago",
		"Europe/London",
		"Australia/Lord_Howe",
		"UTC",
	} {
		if _, err := time.LoadLocation(name); err != nil {
			t.Errorf("time zone %q will not load: %v. cmd/mcpd imports time/tzdata "+
				"so that a backup schedule means the same thing all year inside the "+
				"container; check that the blank import is still there", name, err)
		}
	}
}
