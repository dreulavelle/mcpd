package backup

import (
	"strings"
	"time"
)

// What an archive on a destination is called, and how to read one back.
//
// The name is load-bearing rather than decorative, and it carries three facts.
//
// It says the file is mcpd's, so that a destination which is very often a
// shared folder can be listed at all: a name this cannot parse is invisible,
// and retention can never consider it.
//
// It says *which* mcpd's, because two hosts can be pointed at one bucket. An
// archive whose slug is not this instance's is another host's backup, and is
// invisible for the same reason somebody's holiday photos are.
//
// And it says when, from the name rather than from the file's modified time: a
// NAS with a wrong clock, an S3 copy and a restore from the destination's own
// snapshot all rewrite the second and none of them rewrites the first.

// The grammar is `mcpd-[<slug>-]<timestamp>[-<unique>].mcpdbak`, where the
// timestamp is the only segment that can parse as one -- a slug is lowercase
// letters, digits and dashes, so it can hold neither the `T` nor the `Z`.
const (
	// namePrefix is on every archive mcpd writes.
	namePrefix = "mcpd-"
	// nameLayout is the timestamp, and is the same one PruneSuperseded reads
	// out of a superseded directory. One layout, one parser.
	nameLayout = "20060102T150405Z"
	// uniqueLength is how much of a run's id goes on the end. Long enough that
	// two runs never collide, short enough to keep the name readable.
	uniqueLength = 8
)

// ArchiveName is what a run calls the file it uploads.
//
// The instance slug is in the middle so that two hosts backing up to one bucket
// do not overwrite each other and can still be told apart at a glance. It is
// omitted when the host has no public address to derive one from, which is the
// ordinary state of a deployment nobody has finished configuring.
//
// The unique suffix is on the end because the timestamp is only to the second,
// and two runs in one second -- a schedule firing as somebody presses the
// button, or two manual runs in quick succession -- would otherwise write the
// same name twice. The second upload would replace the first while the history
// showed two successes, which is the worst shape a backup failure can take.
func ArchiveName(instance string, at time.Time, unique string) string {
	var b strings.Builder
	b.WriteString(namePrefix)
	if slug := Slug(instance); slug != "" {
		b.WriteString(slug)
		b.WriteByte('-')
	}
	b.WriteString(at.UTC().Format(nameLayout))
	if suffix := uniqueSuffix(unique); suffix != "" {
		b.WriteByte('-')
		b.WriteString(suffix)
	}
	return b.String() + Extension
}

// uniqueSuffix reduces a run id to something that can sit in a filename on
// every filesystem a destination might be.
//
// Run ids are a kind prefix, an underscore and hex -- `bkr_a1b2...` -- and it
// is the hex that distinguishes one from another, so the prefix is dropped
// rather than repeated in every name. Anything that reduces to nothing is left
// off entirely rather than written as an empty segment.
func uniqueSuffix(unique string) string {
	unique = strings.ToLower(strings.TrimSpace(unique))
	if i := strings.LastIndex(unique, "_"); i >= 0 {
		unique = unique[i+1:]
	}

	var b strings.Builder
	for _, r := range unique {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
		if b.Len() == uniqueLength {
			break
		}
	}
	return b.String()
}

// archive is one of mcpd's own names, taken apart.
type archive struct {
	// Slug is the instance the archive came from, or empty for a host that had
	// no address to derive one from.
	Slug string
	At   time.Time
}

// parseArchive reads a name, and reports whether it is one of mcpd's at all.
func parseArchive(name string) (archive, bool) {
	if !strings.HasPrefix(name, namePrefix) || !strings.HasSuffix(name, Extension) {
		return archive{}, false
	}
	core := name[len(namePrefix) : len(name)-len(Extension)]

	// The timestamp is whichever segment parses as one, and there can only be
	// one: a slug holds no uppercase and a suffix is hex, so neither can carry
	// the `T` and `Z` the layout requires.
	segments := strings.Split(core, "-")
	for i, segment := range segments {
		at, err := time.Parse(nameLayout, segment)
		if err != nil {
			continue
		}
		return archive{Slug: strings.Join(segments[:i], "-"), At: at}, true
	}
	return archive{}, false
}

// TimeFromName reads the instant out of an archive's name, and reports whether
// the name is one of mcpd's at all.
func TimeFromName(name string) (time.Time, bool) {
	parsed, ok := parseArchive(name)
	return parsed.At, ok
}

// Slug reduces a host's public address to something that can sit in a filename
// on every filesystem a destination might be.
//
// Lowercase letters, digits and dashes, nothing else, and never leading or
// trailing dashes. An address with a scheme, a port and a path all collapse to
// the host part a person would recognise.
func Slug(instance string) string {
	s := strings.ToLower(strings.TrimSpace(instance))
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, ":"); i > 0 && !strings.Contains(s[i:], "]") {
		s = s[:i]
	}

	var b strings.Builder
	dash := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			dash = false
		default:
			// Runs of anything else become one dash, and a leading one is
			// dropped, so an address and its IPv6 brackets do not produce a
			// name full of separators.
			if b.Len() > 0 && !dash {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	// Long enough for a hostname, short enough that the name stays readable on
	// a destination that lists in a narrow column.
	if len(out) > 40 {
		out = strings.Trim(out[:40], "-")
	}
	return out
}
