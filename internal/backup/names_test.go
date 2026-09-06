package backup

import (
	"strings"
	"testing"
	"time"
)

// Two runs in the same second get different names.
//
// The timestamp is only to the second, and a schedule firing as somebody
// presses the button puts two runs inside one. Without the run's id on the end
// the second upload replaces the first while the history shows two successes --
// which is the worst shape a backup failure can take, because everything says
// it worked.
func TestTwoRunsInOneSecondDoNotWriteTheSameName(t *testing.T) {
	at := time.Date(2026, 2, 8, 4, 0, 0, 0, time.UTC)
	first := ArchiveName("https://nas.example.com", at, "bkr_aaaaaaaaaaaa")
	second := ArchiveName("https://nas.example.com", at, "bkr_bbbbbbbbbbbb")

	if first == second {
		t.Fatalf("both runs wrote %q", first)
	}
	// And both are still names retention can read the date out of.
	for _, name := range []string{first, second} {
		got, ok := TimeFromName(name)
		if !ok {
			t.Fatalf("%q is not a name retention can parse", name)
		}
		if !got.Equal(at) {
			t.Errorf("%q parsed as %s, want %s", name, got, at)
		}
	}
}

// The name says which instance wrote it, so two hosts sharing a bucket do not
// take each other's backups for their own.
func TestArchiveNameCarriesTheInstanceAndTheTime(t *testing.T) {
	at := time.Date(2026, 2, 8, 4, 0, 0, 0, time.UTC)

	cases := []struct {
		name     string
		instance string
		unique   string
		want     string
		wantSlug string
	}{
		{
			name:     "an ordinary address",
			instance: "https://nas.example.com:8443/mcp",
			unique:   "bkr_0123456789ab",
			want:     "mcpd-nas-example-com-20260208T040000Z-01234567.mcpdbak",
			wantSlug: "nas-example-com",
		},
		{
			name:     "a host with no address configured",
			instance: "",
			unique:   "bkr_0123456789ab",
			want:     "mcpd-20260208T040000Z-01234567.mcpdbak",
		},
		{
			name:     "no run id either",
			instance: "nas",
			want:     "mcpd-nas-20260208T040000Z.mcpdbak",
			wantSlug: "nas",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ArchiveName(tc.instance, at, tc.unique)
			if got != tc.want {
				t.Fatalf("named %q, want %q", got, tc.want)
			}
			parsed, ok := parseArchive(got)
			if !ok {
				t.Fatalf("%q does not parse back", got)
			}
			if parsed.Slug != tc.wantSlug {
				t.Errorf("slug %q, want %q", parsed.Slug, tc.wantSlug)
			}
			if !parsed.At.Equal(at) {
				t.Errorf("time %s, want %s", parsed.At, at)
			}
		})
	}
}

// A destination is usually a shared folder, so a name that is not one of
// mcpd's must be invisible: retention reads this listing.
func TestTimeFromNameRefusesAnythingThatIsNotOurs(t *testing.T) {
	for _, name := range []string{
		"",
		"family-photos.zip",
		"notes.txt",
		".mcpd-check",
		// The right shape and the wrong extension: a partial upload.
		"mcpd-nas-20260208T040000Z.mcpdbak.part",
		// No separator before the timestamp, so the leading text is not a slug.
		"mcpd-notes20260208T040000Z.mcpdbak",
		// Not a timestamp at all.
		"mcpd-nas-notatime.mcpdbak",
		"mcpd-nas-20261332T040000Z.mcpdbak",
		// The prefix somebody else's tool might use.
		"mcpdbackup-20260208T040000Z.mcpdbak",
	} {
		if _, ok := TimeFromName(name); ok {
			t.Errorf("%q was read as one of mcpd's archives", name)
		}
	}
}

// The unique suffix is reduced to characters a filename can hold anywhere, and
// left off entirely when there is nothing usable in it.
func TestTheUniqueSuffixIsSafeInAFilename(t *testing.T) {
	at := time.Date(2026, 2, 8, 4, 0, 0, 0, time.UTC)
	name := ArchiveName("nas", at, "BKR_../../etc/passwd")
	if strings.ContainsAny(name, "/._") && !strings.HasSuffix(name, ".mcpdbak") {
		t.Errorf("%q holds something a path should not", name)
	}
	if strings.Contains(strings.TrimSuffix(name, Extension), ".") ||
		strings.Contains(name, "/") {
		t.Errorf("%q holds a path separator or a stray dot", name)
	}
	if _, ok := TimeFromName(name); !ok {
		t.Errorf("%q does not parse back", name)
	}

	// Nothing usable at all leaves the name as it was before suffixes existed,
	// which is still a name that parses.
	bare := ArchiveName("nas", at, "___")
	if bare != "mcpd-nas-20260208T040000Z.mcpdbak" {
		t.Errorf("named %q", bare)
	}
}

func TestSlug(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{in: "https://nas.example.com", want: "nas-example-com"},
		{in: "https://nas.example.com:8443/mcp?x=1", want: "nas-example-com"},
		{in: "HTTPS://NAS.example.com", want: "nas-example-com"},
		{in: "http://[2001:db8::1]:443/", want: "2001-db8-1"},
		{in: "", want: ""},
		{in: "...", want: ""},
		{in: strings.Repeat("a", 60), want: strings.Repeat("a", 40)},
	} {
		if got := Slug(tc.in); got != tc.want {
			t.Errorf("Slug(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
