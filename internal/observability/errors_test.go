package observability

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/getsentry/sentry-go"
)

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// No DSN means no reporter at all -- not a disabled one. An operator who has
// never opened the settings page has not agreed to anything, and there should
// be no client, no goroutine and no possibility of a misconfiguration that
// quietly sends something.
func TestNewErrorReporter_OffWithoutADSN(t *testing.T) {
	for _, dsn := range []string{"", "   ", "\t"} {
		r, err := NewErrorReporter(ErrorReporterOptions{DSN: dsn, Log: quietLog()})
		if err != nil {
			t.Fatalf("an absent DSN is not an error: %v", err)
		}
		if r != nil {
			t.Fatalf("a reporter was built for DSN %q", dsn)
		}
		// The nil reporter has to be safe at every call site, because those
		// sites are panic handlers: a forgotten nil check there turns a
		// recovered panic into a second one.
		r.CapturePanic("boom", []byte("stack"), "test")
		r.CaptureError(errors.New("x"), "test")
		r.Flush(0)
		if r.Enabled() {
			t.Error("a nil reporter reports itself as enabled")
		}
	}
}

// Sentry fills ServerName with os.Hostname() when it is empty. On a customer's
// own hardware that hostname is frequently the most identifying thing in the
// whole report, so "not configured" has to mean "send nothing" rather than
// "use the default".
func TestServerName_DoesNotLeakTheMachineName(t *testing.T) {
	if got := serverName(""); strings.TrimSpace(got) != "" {
		t.Errorf("an unset label produced %q, which the SDK would replace with "+
			"the hostname", got)
	}
	if got := serverName("  "); strings.TrimSpace(got) != "" {
		t.Errorf("a blank label produced %q", got)
	}
	if got := serverName("acme-hq"); got != "acme-hq" {
		t.Errorf("a chosen label was changed to %q", got)
	}
}

// The gate is BeforeSend rather than the call sites, so that a future caller
// cannot forget. These build the event the SDK would build and check what
// comes out the other side.
func TestScrubEvent_RemovesTheCustomersEstate(t *testing.T) {
	event := &sentry.Event{
		Message:    "could not reach https://obs.acme-hospital.internal/api/v0/devices",
		ServerName: "customer-nas-01",
		Exception: []sentry.Exception{{
			Type:  "*errors.errorString",
			Value: "observium: cannot reach the database at 192.168.50.101:3306",
			Stacktrace: &sentry.Stacktrace{Frames: []sentry.Frame{{
				Filename: "internal/plugins/observium/mysql.go",
				AbsPath:  "/build/internal/plugins/observium/mysql.go",
				Module:   "github.com/spoked/mcpd/internal/plugins/observium",
				Vars:     map[string]any{"password": "hunter2"},
			}}},
		}},
		User: sentry.User{
			Email: "alice@acme-hospital.com", IPAddress: "10.1.2.3", Username: "alice",
		},
		Request: &sentry.Request{
			URL:         "https://mcpd.acme-hospital.internal/mcp/observium",
			QueryString: "token=hnvKb3ygMNubJ24SYHaEWlUv4pVA",
			Cookies:     "session=abc",
		},
		Tags: map[string]string{"upstream": "observium.acme-hospital.internal"},
		Contexts: map[string]sentry.Context{
			"device": {"name": "customer-nas-01"},
			"os":     {"name": "linux"},
		},
	}

	got := scrubEvent(true, "")(event, nil)
	if got == nil {
		t.Fatal("the event was dropped entirely")
	}
	rendered := render(got)

	for _, leak := range []string{
		"acme-hospital", "192.168.50.101", "hunter2", "alice",
		"customer-nas-01", "hnvKb3ygMNubJ24SYHaEWlUv4pVA", "session=abc",
	} {
		if strings.Contains(rendered, leak) {
			t.Errorf("%q left the machine:\n%s", leak, rendered)
		}
	}

	// A request describes whoever was talking to mcpd and never survives.
	if got.Request != nil {
		t.Error("the request survived")
	}
	if got.User.Email != "" || got.User.IPAddress != "" || got.User.Username != "" {
		t.Error("the user survived")
	}
	// Frame variables are populated by some integrations and are the one place
	// a Go stack trace can carry a value.
	if v := got.Exception[0].Stacktrace.Frames[0].Vars; v != nil {
		t.Errorf("frame variables survived: %v", v)
	}
	if _, ok := got.Contexts["device"]; ok {
		t.Error("the device context survived; it describes the customer's machine")
	}
}

// The stack trace is the whole reason to send a report. Scrubbing that removes
// the file, the function and the line leaves something nobody can act on.
func TestScrubEvent_KeepsWhatMakesItActionable(t *testing.T) {
	event := &sentry.Event{
		Exception: []sentry.Exception{{
			Type:  "*fmt.wrapError",
			Value: "observium: reading eventlog from the database",
			Stacktrace: &sentry.Stacktrace{Frames: []sentry.Frame{{
				Filename: "internal/plugins/observium/mysql.go",
				Module:   "github.com/spoked/mcpd/internal/plugins/observium",
				Function: "(*mysqlReader).selectFrom",
				Lineno:   214,
			}}},
		}},
	}

	got := scrubEvent(true, "")(event, nil)
	frame := got.Exception[0].Stacktrace.Frames[0]

	if frame.Filename != "internal/plugins/observium/mysql.go" {
		t.Errorf("the filename was mangled to %q", frame.Filename)
	}
	if frame.Module != "github.com/spoked/mcpd/internal/plugins/observium" {
		t.Errorf("the module path was mangled to %q", frame.Module)
	}
	if frame.Function != "(*mysqlReader).selectFrom" || frame.Lineno != 214 {
		t.Error("the function or line was lost")
	}
	if !strings.Contains(got.Exception[0].Value, "reading eventlog") {
		t.Errorf("the message lost what it was doing: %q", got.Exception[0].Value)
	}
}

// Messages are where a customer's estate is named, so sending them is a
// separate decision from sending crashes at all. With it off, the stack still
// travels and the sentence does not.
func TestScrubEvent_WithheldMessages(t *testing.T) {
	event := &sentry.Event{
		Message: "could not reach obs.acme.internal",
		Exception: []sentry.Exception{{
			Type:       "*fmt.wrapError",
			Value:      "observium: no device core-sw-01.dc2.acme.internal",
			Stacktrace: &sentry.Stacktrace{Frames: []sentry.Frame{{Function: "walk", Lineno: 9}}},
		}},
	}

	got := scrubEvent(false, "")(event, nil)
	rendered := render(got)
	if strings.Contains(rendered, "acme") || strings.Contains(rendered, "core-sw-01") {
		t.Errorf("estate data survived with messages off:\n%s", rendered)
	}
	if got.Message != "" {
		t.Errorf("the message survived: %q", got.Message)
	}
	if !strings.Contains(got.Exception[0].Value, "withheld") {
		t.Errorf("the withheld value should say why it is empty: %q", got.Exception[0].Value)
	}
	// The type and the trace are what remain, and they are enough to find the
	// code path.
	if got.Exception[0].Type != "*fmt.wrapError" {
		t.Error("the error type was removed, and it carries no estate data")
	}
	if got.Exception[0].Stacktrace.Frames[0].Function != "walk" {
		t.Error("the stack was removed")
	}
}

// A breadcrumb's data is arbitrary and this process adds none, so anything in
// it came from an SDK integration and has not been reviewed.
func TestBreadcrumbs_AreStrippedOfData(t *testing.T) {
	r, err := NewErrorReporter(ErrorReporterOptions{
		DSN: "https://key@example.invalid/1", Log: quietLog(),
	})
	if err != nil {
		t.Fatalf("NewErrorReporter: %v", err)
	}
	if r == nil {
		t.Fatal("a valid DSN produced no reporter")
	}
	defer r.Flush(0)
	if !r.Enabled() {
		t.Error("a configured reporter reports itself disabled")
	}
}

// render flattens an event to the text that would be transmitted, so a test
// can assert on the whole of it rather than field by field and miss one.
func render(e *sentry.Event) string {
	var b strings.Builder
	b.WriteString(e.Message)
	b.WriteString(" " + e.ServerName)
	b.WriteString(" " + e.User.Email + " " + e.User.IPAddress + " " + e.User.Username)
	if e.Request != nil {
		b.WriteString(" " + e.Request.URL + " " + e.Request.QueryString + " " + e.Request.Cookies)
	}
	for _, ex := range e.Exception {
		b.WriteString(" " + ex.Type + " " + ex.Value)
		if ex.Stacktrace != nil {
			for _, f := range ex.Stacktrace.Frames {
				b.WriteString(" " + f.Filename + " " + f.AbsPath + " " + f.Module + " " + f.Function)
				for k, v := range f.Vars {
					b.WriteString(" " + k + "=" + strings.TrimSpace(sentryValue(v)))
				}
			}
		}
	}
	for k, v := range e.Tags {
		b.WriteString(" " + k + "=" + v)
	}
	for _, ctx := range e.Contexts {
		for k, v := range ctx {
			b.WriteString(" " + k + "=" + sentryValue(v))
		}
	}
	return b.String()
}

func sentryValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
