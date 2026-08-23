package settings

import (
	"context"
	"time"
)

// A field declares its own default and its own unit, and these read both.
//
// The alternative is every caller repeating the number, which is how the
// dashboard came to show one default and the host to run with another. There
// is one declaration; this is how it is read.

// unitOf returns what a duration field counts in. Empty means minutes, which
// is what every duration meant before any of them was counted in anything
// else.
func unitOf(f Field) time.Duration {
	switch f.Unit {
	case UnitSeconds:
		return time.Second
	case UnitHours:
		return time.Hour
	default:
		return time.Minute
	}
}

// DefaultDuration returns a duration field's declared default.
func DefaultDuration(key string) time.Duration {
	f, ok := FieldFor(key)
	if !ok {
		return 0
	}
	n, _ := f.Default.(int)
	return time.Duration(n) * unitOf(f)
}

// DefaultString returns a string or enum field's declared default.
func DefaultString(key string) string {
	f, ok := FieldFor(key)
	if !ok {
		return ""
	}
	s, _ := f.Default.(string)
	return s
}

// DefaultBool returns a bool field's declared default.
func DefaultBool(key string) bool {
	f, ok := FieldFor(key)
	if !ok {
		return false
	}
	b, _ := f.Default.(bool)
	return b
}

// FieldDuration reads a duration field, in its own unit, falling back to its
// declared default.
func (s *Store) FieldDuration(ctx context.Context, key string) time.Duration {
	f, ok := FieldFor(key)
	if !ok {
		return 0
	}
	return s.Duration(ctx, key, unitOf(f), DefaultDuration(key))
}

// FieldBool reads a bool field, falling back to its declared default.
func (s *Store) FieldBool(ctx context.Context, key string) bool {
	return s.Bool(ctx, key, DefaultBool(key))
}

// FieldString reads a string or enum field, falling back to its declared
// default.
func (s *Store) FieldString(ctx context.Context, key string) string {
	return s.String(ctx, key, DefaultString(key))
}
