package observability

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
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
	log, _ := NewSwitchableLogger(w, level, format)
	return log
}

// NewSwitchableLogger builds the application logger together with the control
// that changes how much it says and in what shape.
//
// Both of those are settings now, and settings live in the database -- which
// is not open yet when the logger has to exist. The alternatives were to leave
// logging in the startup file, or to have mcpd open its own database twice.
// This is the third: the logger is built with both handlers at once and picks
// between them per record, so the moment the settings are read the change
// takes effect, with nothing to restart and nothing pretending it did.
func NewSwitchableLogger(w io.Writer, level slog.Level, format string) (*slog.Logger, *LogControl) {
	log, ctl, _ := NewStreamingLogger(w, level, format, false)
	return log, ctl
}

// NewStreamingLogger is NewSwitchableLogger with an optional copy of the log
// kept for the dashboard to show.
//
// The copy is rendered by a handler of its own rather than taken from the
// destination's bytes, for two reasons. The dashboard needs JSON whatever
// format an operator has chosen for the file -- those are different audiences,
// and only one of them can be asked to cope with a change. And a handler built
// from the same options carries the same level filtering and the same
// redaction, so a value withheld from the file cannot reach a browser by this
// route.
//
// It is opt-in because it is not free: a line that is streamed is formatted
// twice. A host with nobody watching should not pay for the page nobody opened,
// which is why the tap is nil unless asked for.
func NewStreamingLogger(w io.Writer, level slog.Level, format string, stream bool) (
	*slog.Logger, *LogControl, *LogStream,
) {
	ctl := &LogControl{}
	ctl.level.Set(level)
	ctl.text.Store(strings.EqualFold(format, "text"))

	opts := &slog.HandlerOptions{
		Level:       &ctl.level,
		ReplaceAttr: redactAttr,
	}
	// Both handlers write to one destination, and each of them guards its
	// writer with a mutex of its own -- so between the two of them there is no
	// lock at all. The shared one below is what makes a record that is still
	// being written as JSON and one starting as text take turns.
	dst := &syncWriter{w: w}
	h := switchHandler{
		json: slog.NewJSONHandler(dst, opts),
		text: slog.NewTextHandler(dst, opts),
		ctl:  ctl,
	}

	var ls *LogStream
	if stream {
		ls = NewLogStream()
		// Its own writer, and its own lock. The stream never shares the
		// destination's, so a slow disk and a watching browser cannot wait on
		// each other.
		h.tap = slog.NewJSONHandler(ls, opts)
	}
	// Wrapped so that a record written with a context carries the correlation
	// ID the caller was handed. Outermost, so it sees the record before any
	// handler that might drop attributes.
	return slog.New(contextHandler{Handler: h}), ctl, ls
}

// syncWriter serialises the writes of every handler that shares it.
//
// A format change does not wait for the records already in flight, so the two
// handlers overlap by design rather than by accident. Without this the
// overlap is an unsynchronised write to the same io.Writer: interleaved lines
// at best, and whatever the writer does with concurrent calls at worst.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// LogControl changes the level and the format of a running logger.
type LogControl struct {
	level slog.LevelVar
	text  atomic.Bool
}

// Set applies a level and a format. Safe from any goroutine, which it has to
// be: the change arrives on a dashboard request while every other goroutine is
// logging.
func (c *LogControl) Set(level slog.Level, format string) {
	if c == nil {
		return
	}
	c.level.Set(level)
	c.text.Store(strings.EqualFold(format, "text"))
}

// switchHandler dispatches each record to whichever handler the format
// currently names.
//
// Both handlers are built up front and derived together, so log.With(...) on
// one is log.With(...) on the other and a format change mid-life does not lose
// the attributes a component was given when it was wired.
type switchHandler struct {
	json slog.Handler
	text slog.Handler
	// tap receives every record the destination does, for the dashboard. Nil
	// on a host where nobody asked for it.
	tap slog.Handler
	ctl *LogControl
}

func (h switchHandler) pick() slog.Handler {
	if h.ctl.text.Load() {
		return h.text
	}
	return h.json
}

func (h switchHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.pick().Enabled(ctx, l)
}

func (h switchHandler) Handle(ctx context.Context, r slog.Record) error {
	err := h.pick().Handle(ctx, r)
	if h.tap != nil {
		// After the destination, and its failure is not the caller's problem:
		// the log that matters is the one that leaves the process, and a page
		// nobody may even have open must not turn a logged line into an error
		// somewhere up the stack.
		_ = h.tap.Handle(ctx, r)
	}
	return err
}

func (h switchHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := switchHandler{
		json: h.json.WithAttrs(attrs), text: h.text.WithAttrs(attrs), ctl: h.ctl,
	}
	if h.tap != nil {
		// Derived alongside the others, or a component given attributes when
		// it was wired would appear on the dashboard without them.
		out.tap = h.tap.WithAttrs(attrs)
	}
	return out
}

func (h switchHandler) WithGroup(name string) slog.Handler {
	out := switchHandler{
		json: h.json.WithGroup(name), text: h.text.WithGroup(name), ctl: h.ctl,
	}
	if h.tap != nil {
		out.tap = h.tap.WithGroup(name)
	}
	return out
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
