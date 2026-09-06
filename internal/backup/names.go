package backup

import (
	"strings"
	"time"
)

// What an archive on a destination is called, and how to read one back.
//
// The name is load-bearing rather than decorative. A destination is very often
// a shared folder, so every listing is filtered through this: a file whose name
// this cannot parse is invisible to mcpd, and retention can never consider it.
// And the timestamp comes from the name rather than from the file's modified
// time, because a NAS with a wrong clock, an S3 copy, or a restore from the
// destination's own snapshot all rewrite the second and none of them rewrites
// the first.

// namePrefix is on every archive mcpd writes.
const namePrefix = "mcpd-"

// nameLayout is the timestamp, and is the same one PruneSuperseded reads out of
// a superseded directory. One layout, one parser.
const nameLayout = "20060102T150405Z"

// ArchiveName is what a run calls the file it uploads.
//
// The instance slug is in the middle so that two hosts backing up to one bucket
// do not overwrite each other and can still be told apart at a glance. It is
// omitted when the host has no public address to derive one from, which is the
// ordinary state of a deployment nobody has finished configuring.
func ArchiveName(instance string, at time.Time) string {
	slug := Slug(instance)
	stamp := at.UTC().Format(nameLayout)
	if slug == "" {
		return namePrefix + stamp + Extension
	}
	return namePrefix + slug + "-" + stamp + Extension
}

// TimeFromName reads the instant out of an archive's name, and reports whether
// the name is one of mcpd's at all.
func TimeFromName(name string) (time.Time, bool) {
	if !strings.HasPrefix(name, namePrefix) || !strings.HasSuffix(name, Extension) {
		return time.Time{}, false
	}
	core := name[len(namePrefix) : len(name)-len(Extension)]
	if len(core) < len(nameLayout) {
		return time.Time{}, false
	}
	stamp := core[len(core)-len(nameLayout):]
	// Whatever precedes the timestamp is the slug, and it must end in the
	// separator. Without this check `mcpd-notes20260101T000000Z.mcpdbak` --
	// which nothing here wrote -- would parse.
	if rest := core[:len(core)-len(nameLayout)]; rest != "" && !strings.HasSuffix(rest, "-") {
		return time.Time{}, false
	}
	at, err := time.Parse(nameLayout, stamp)
	if err != nil {
		return time.Time{}, false
	}
	return at, true
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
