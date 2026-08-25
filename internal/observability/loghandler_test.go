package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// The correlation ID is returned to the caller in a response header and in the
// error body, so it is the one thing a person on a machine nobody here can
// reach is able to quote back. Until the handler put it on records, quoting it
// found nothing: the middleware tagged a request logger that almost no call
// site used.
func TestContextHandler_PutsTheCorrelationIDOnTheRecord(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(&buf, slog.LevelInfo, "json")

	ctx := WithCorrelationID(context.Background(), "abc123")
	log.ErrorContext(ctx, "upstream refused the call", "plugin", "observium")

	var got map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &got); err != nil {
		t.Fatalf("log line is not JSON: %v\n%s", err, buf.String())
	}
	if got["correlation_id"] != "abc123" {
		t.Fatalf("correlation_id = %v, want abc123\n%s", got["correlation_id"], buf.String())
	}
	if got["plugin"] != "observium" {
		t.Error("the call's own attributes were lost")
	}
}

// A logger built by Correlate already carries the ID as a With attribute.
// Adding it again produces a duplicate key, which some consumers render as an
// array and others silently drop.
func TestContextHandler_DoesNotDuplicateAnExistingID(t *testing.T) {
	var buf bytes.Buffer
	base := NewLogger(&buf, slog.LevelInfo, "json")
	tagged := base.With("correlation_id", "from-middleware")

	ctx := WithCorrelationID(context.Background(), "from-context")
	tagged.ErrorContext(ctx, "something failed")

	if n := strings.Count(buf.String(), "correlation_id"); n != 1 {
		t.Fatalf("correlation_id appears %d times, want 1:\n%s", n, buf.String())
	}
	if !strings.Contains(buf.String(), "from-middleware") {
		t.Error("the request-scoped value should win; it is the one the caller was given")
	}
}

// Without a context there is nothing to read, and slog hands the handler
// context.Background(). That is a real constraint rather than a bug: no
// handler can recover what was not passed, which is why call sites use the
// *Context variants.
func TestContextHandler_QuietWithoutAContext(t *testing.T) {
	var buf bytes.Buffer
	NewLogger(&buf, slog.LevelInfo, "json").Error("no context here")

	if strings.Contains(buf.String(), "correlation_id") {
		t.Errorf("a correlation_id appeared from nowhere:\n%s", buf.String())
	}
}

// Redaction still applies. The handler wrapping must not bypass the
// ReplaceAttr that keeps credentials out of the log.
func TestContextHandler_KeepsRedactionWorking(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(&buf, slog.LevelInfo, "json")
	log.ErrorContext(WithCorrelationID(context.Background(), "x"),
		"auth failed", "token", "hnvKb3ygMNubJ24SYHaEWlUv4pVA")

	if strings.Contains(buf.String(), "hnvKb3ygMNubJ24SYHaEWlUv4pVA") {
		t.Fatalf("a credential reached the log:\n%s", buf.String())
	}
}

// A crash report says where a panic landed. The breadcrumbs say what was
// happening first, which is usually the more useful half.
func TestBreadcrumbs_WarningsAndErrorsAreRecorded(t *testing.T) {
	c := newCollector(t)
	r, err := NewErrorReporter(ErrorReporterOptions{
		DSN: c.dsn(), IncludeMessages: true, Log: quietLog(),
	})
	if err != nil || r == nil {
		t.Fatalf("NewErrorReporter: %v", err)
	}

	var buf bytes.Buffer
	log := AttachBreadcrumbs(NewLogger(&buf, slog.LevelDebug, "json"), r)

	log.Info("this is routine and should not be a breadcrumb")
	log.Warn("upstream is slow", "plugin", "observium")
	log.Error("upstream refused", "plugin", "observium", "correlation_id", "abc123")

	r.CapturePanic("boom", nil, "test")
	r.Flush(5 * time.Second)

	sent := c.transmitted()
	for _, want := range []string{"upstream is slow", "upstream refused", "abc123"} {
		if !strings.Contains(sent, want) {
			t.Errorf("%q was not attached to the report:\n%s", want, sent)
		}
	}
	// Info is not a breadcrumb. A working system produces a great many of
	// them and they would push the warnings out of a bounded buffer.
	if strings.Contains(sent, "this is routine") {
		t.Error("an info line became a breadcrumb")
	}
	// The log itself is unaffected -- it is the record that always exists,
	// whether or not anybody configured a collector.
	if !strings.Contains(buf.String(), "this is routine") {
		t.Error("attaching breadcrumbs changed what the log says")
	}
}

// Breadcrumb text is the same kind of text as a log message and gets the same
// treatment. The redaction that runs for log output is a ReplaceAttr on the
// writing handler and does not reach this path.
func TestBreadcrumbs_AreScrubbed(t *testing.T) {
	c := newCollector(t)
	r, _ := NewErrorReporter(ErrorReporterOptions{
		DSN: c.dsn(), IncludeMessages: true, Log: quietLog(),
	})
	if r == nil {
		t.Fatal("no reporter")
	}

	log := AttachBreadcrumbs(NewLogger(&bytes.Buffer{}, slog.LevelDebug, "json"), r)
	log.Error("could not reach https://obs.acme-hospital.internal/api",
		"plugin", "observium")

	r.CapturePanic("boom", nil, "test")
	r.Flush(5 * time.Second)

	if strings.Contains(c.transmitted(), "acme-hospital") {
		t.Errorf("a breadcrumb carried the customer's estate:\n%s", c.transmitted())
	}
}

// Reporting off is the normal case, and it must cost nothing and change
// nothing.
func TestAttachBreadcrumbs_NilReporterIsTheSameLogger(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(&buf, slog.LevelInfo, "json")
	if got := AttachBreadcrumbs(log, nil); got != log {
		t.Error("a nil reporter produced a different logger")
	}
}
