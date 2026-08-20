package observability

import (
	"io"
	"log/slog"
	"strings"
)

// redactedKeys name log attributes whose values must never be emitted.
// Matching is on a normalised form of the key, so "API-Key", "api_key" and
// "apiKey" are all caught.
var redactedKeys = map[string]bool{
	"authorization": true,
	"auth":          true,
	"apikey":        true,
	"api":           true,
	"token":         true,
	"accesstoken":   true,
	"refreshtoken":  true,
	"bearer":        true,
	"password":      true,
	"passwd":        true,
	"secret":        true,
	"clientsecret":  true,
	"credential":    true,
	"credentials":   true,
	"cookie":        true,
	"setcookie":     true,
	"privatekey":    true,
	"sessionkey":    true,
}

// Redacted replaces a sensitive value in log output.
const Redacted = "[REDACTED]"

// NewLogger builds the application logger. format is "json" or "text"; JSON is
// the default because production output is consumed by journald and log
// shippers, not read by eye.
func NewLogger(w io.Writer, level slog.Level, format string) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level:       level,
		ReplaceAttr: redactAttr,
	}
	var h slog.Handler
	if strings.EqualFold(format, "text") {
		h = slog.NewTextHandler(w, opts)
	} else {
		h = slog.NewJSONHandler(w, opts)
	}
	return slog.New(h)
}

// redactAttr is the ReplaceAttr hook that censors sensitive values.
func redactAttr(_ []string, a slog.Attr) slog.Attr {
	if redactedKeys[normalizeKey(a.Key)] {
		return slog.String(a.Key, Redacted)
	}
	return a
}

// normalizeKey lowercases a key and strips separators so that spelling
// variants map to the same entry.
func normalizeKey(k string) string {
	var b strings.Builder
	b.Grow(len(k))
	for _, r := range k {
		switch {
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		case r == '-' || r == '_' || r == ' ' || r == '.':
			// separators dropped
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ParseLevel maps a configured level name to a slog.Level, defaulting to info.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
