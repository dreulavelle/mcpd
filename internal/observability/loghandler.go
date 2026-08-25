package observability

import (
	"context"
	"log/slog"
	"sync"

	"github.com/getsentry/sentry-go"
)

// Two things wrap the log handler, and both exist for the same support call:
// somebody on a machine nobody here can reach says mcpd did something wrong.
//
// contextHandler puts the correlation ID on every record written with a
// context. That ID is already returned to the caller in a header and in the
// error body, so it is the one thing a person can quote back -- and until this
// existed, quoting it found nothing, because the middleware tagged a logger
// almost nothing used.
//
// breadcrumbHandler keeps the last few warnings and errors so that a crash
// report says what led up to the crash rather than only where it landed. A
// stack trace answers "where"; the run-up answers "doing what". It does
// nothing at all when crash reporting is off, which is the normal case.

// breadcrumbCategory marks the breadcrumbs this process creates, so the gate
// in errors.go can tell them from whatever an SDK integration added and keep
// their data rather than dropping it unreviewed.
const breadcrumbCategory = "log"

// contextHandler copies request-scoped facts out of the context and onto the
// record.
//
// It only fires for the *Context log methods, because those are the only ones
// slog gives a context to. That is a real constraint rather than an oversight
// in this design: log.Error(...) hands the handler context.Background(), and
// no handler can recover what was not passed. The convention this asks of call
// sites is therefore ErrorContext(ctx, ...) wherever a ctx is in scope.
type contextHandler struct {
	slog.Handler
	// tagged records that a correlation_id arrived through With() rather than
	// on the call.
	//
	// It cannot be discovered from the record: slog.Record.Attrs iterates the
	// attributes of *this call* only, and a With attribute lives in the
	// handler chain where a record cannot see it. Correlate builds exactly
	// such a logger, so without this flag every request-scoped log line
	// carried correlation_id twice -- which some consumers render as an array
	// and others silently drop.
	tagged bool
}

func (h contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if !h.tagged {
		if id := CorrelationID(ctx); id != "" && !hasAttr(r, "correlation_id") {
			r.AddAttrs(slog.String("correlation_id", id))
		}
	}
	return h.Handler.Handle(ctx, r)
}

func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	tagged := h.tagged
	for _, a := range attrs {
		if a.Key == "correlation_id" {
			tagged = true
		}
	}
	return contextHandler{Handler: h.Handler.WithAttrs(attrs), tagged: tagged}
}

func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{Handler: h.Handler.WithGroup(name), tagged: h.tagged}
}

// hasAttr reports whether this call carried the key. It cannot see With
// attributes, which is what contextHandler.tagged exists for.
func hasAttr(r slog.Record, key string) bool {
	found := false
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			found = true
			return false
		}
		return true
	})
	return found
}

// breadcrumbHandler records warnings and errors as Sentry breadcrumbs.
//
// Breadcrumbs rather than events. An error log is not a crash: this project
// logs one when an upstream is unreachable or a proposal is refused, which are
// things a working system does. Sending each as an event would fill a
// collector with normal behaviour and carry a customer's estate with it. Kept
// as breadcrumbs, they cost nothing until something actually panics -- and
// then they are attached to the report that matters.
//
// The whole thing is inert when reporting is off, which is the default: no
// hub, no allocation, no work in Handle beyond a nil check.
type breadcrumbHandler struct {
	slog.Handler
	hub *sentry.Hub
	// mu guards nothing in the SDK -- Sentry's hub is safe -- but the level
	// check and the breadcrumb build are cheap enough that serialising them
	// beats reasoning about a race that does not exist.
	mu *sync.Mutex
}

func (h breadcrumbHandler) Handle(ctx context.Context, r slog.Record) error {
	if h.hub != nil && r.Level >= slog.LevelWarn {
		h.record(r)
	}
	return h.Handler.Handle(ctx, r)
}

func (h breadcrumbHandler) record(r slog.Record) {
	data := make(map[string]any, 4)
	r.Attrs(func(a slog.Attr) bool {
		// Attribute values are the same kind of text as a log message and get
		// the same treatment. The redaction that runs for the log output is a
		// ReplaceAttr on the writing handler and does not apply here, so this
		// scrubs rather than assuming somebody upstream did.
		switch a.Key {
		case "correlation_id", "component", "plugin", "tool", "operation_id",
			"state", "action", "risk", "outcome", "endpoint", "status":
			// An allow-list, not a filter. Anything not named here is a key
			// this has not considered, and a breadcrumb is not worth guessing
			// about -- these are the ones that identify a code path rather
			// than a customer's equipment.
			data[a.Key] = Scrub(a.Value.String())
		}
		return true
	})

	level := sentry.LevelWarning
	if r.Level >= slog.LevelError {
		level = sentry.LevelError
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	h.hub.AddBreadcrumb(&sentry.Breadcrumb{
		Type:      "default",
		Category:  breadcrumbCategory,
		Message:   Scrub(r.Message),
		Level:     level,
		Data:      data,
		Timestamp: r.Time,
	}, nil)
}

func (h breadcrumbHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return breadcrumbHandler{h.Handler.WithAttrs(attrs), h.hub, h.mu}
}

func (h breadcrumbHandler) WithGroup(name string) slog.Handler {
	return breadcrumbHandler{h.Handler.WithGroup(name), h.hub, h.mu}
}

// AttachBreadcrumbs returns a logger whose warnings and errors also become
// breadcrumbs on the reporter's hub.
//
// The logger is returned rather than mutated because slog handlers are
// immutable by design, and because the caller decides when reporting exists:
// the reporter is built from settings, which is after the logger.
//
// A nil reporter returns the logger unchanged, so the caller does not branch.
func AttachBreadcrumbs(log *slog.Logger, r *ErrorReporter) *slog.Logger {
	if log == nil || r == nil {
		return log
	}
	return slog.New(breadcrumbHandler{
		Handler: log.Handler(),
		hub:     r.hub,
		mu:      &sync.Mutex{},
	})
}
