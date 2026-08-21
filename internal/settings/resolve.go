package settings

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// Resolved values read from the store, falling back to what the bootstrap file
// supplied.
//
// The store is authoritative when it holds a value. That ordering is the whole
// point: a setting changed in the dashboard has to win over the file, or the
// dashboard is doing nothing.

// String returns a stored string, or fallback.
func (s *Store) String(ctx context.Context, key, fallback string) string {
	var v string
	if ok, err := s.GetJSON(ctx, key, &v); err == nil && ok {
		return v
	}
	// A value written before typed encoding, or by hand, may be bare.
	if raw, ok, err := s.Get(ctx, key); err == nil && ok {
		return strings.Trim(raw, `"`)
	}
	return fallback
}

// Bool returns a stored boolean, or fallback.
func (s *Store) Bool(ctx context.Context, key string, fallback bool) bool {
	raw, ok, err := s.Get(ctx, key)
	if err != nil || !ok {
		return fallback
	}
	parsed, err := strconv.ParseBool(strings.Trim(raw, `"`))
	if err != nil {
		return fallback
	}
	return parsed
}

// Int returns a stored integer, or fallback.
func (s *Store) Int(ctx context.Context, key string, fallback int) int {
	raw, ok, err := s.Get(ctx, key)
	if err != nil || !ok {
		return fallback
	}
	parsed, err := strconv.Atoi(strings.Trim(raw, `"`))
	if err != nil {
		return fallback
	}
	return parsed
}

// Minutes returns a stored duration expressed in minutes, or fallback.
//
// Durations are stored as whole minutes rather than Go duration strings
// because the dashboard renders them as a number box, and a value a person
// typed should round-trip as the number they typed.
func (s *Store) Minutes(ctx context.Context, key string, fallback time.Duration) time.Duration {
	n := s.Int(ctx, key, -1)
	if n <= 0 {
		return fallback
	}
	return time.Duration(n) * time.Minute
}

// Strings returns a stored list, or fallback.
func (s *Store) Strings(ctx context.Context, key string, fallback []string) []string {
	var v []string
	if ok, err := s.GetJSON(ctx, key, &v); err == nil && ok && len(v) > 0 {
		return v
	}
	return fallback
}

// Secret returns a stored credential, or fallback.
func (s *Store) Secret(ctx context.Context, key, fallback string) string {
	if raw, ok, err := s.Get(ctx, key); err == nil && ok && raw != "" {
		return raw
	}
	return fallback
}
