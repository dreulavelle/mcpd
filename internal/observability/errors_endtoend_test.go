package observability

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// collector stands in for Sentry or GlitchTip and keeps what was transmitted.
//
// The point of testing against the wire rather than against the event struct
// is that the struct is what this package believes it is sending. The bytes
// are what actually leaves the machine, and a field added by the SDK between
// BeforeSend and the socket would show up only here.
type collector struct {
	mu   sync.Mutex
	body []string
	srv  *httptest.Server
}

func newCollector(t *testing.T) *collector {
	t.Helper()
	c := &collector{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reader io.Reader = r.Body
		if r.Header.Get("Content-Encoding") == "gzip" {
			gz, err := gzip.NewReader(r.Body)
			if err == nil {
				defer gz.Close()
				reader = gz
			}
		}
		raw, _ := io.ReadAll(reader)
		c.mu.Lock()
		c.body = append(c.body, string(raw))
		c.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"test"}`))
	}))
	t.Cleanup(c.srv.Close)
	return c
}

// dsn points the SDK at the fake collector.
func (c *collector) dsn() string {
	return strings.Replace(c.srv.URL, "http://", "http://publickey@", 1) + "/1"
}

func (c *collector) transmitted() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.body, "\n")
}

// A panic reaches the collector, and the customer's estate does not.
func TestEndToEnd_PanicIsReportedAndScrubbed(t *testing.T) {
	c := newCollector(t)
	r, err := NewErrorReporter(ErrorReporterOptions{
		DSN: c.dsn(), Environment: "test", Release: "v1.2.3",
		IncludeMessages: true, Synchronous: true, Log: quietLog(),
	})
	if err != nil {
		t.Fatalf("NewErrorReporter: %v", err)
	}
	if r == nil {
		t.Fatal("no reporter was built for a valid DSN")
	}

	stack := []byte(`goroutine 7 [running]:
github.com/spoked/mcpd/internal/plugins/observium.(*Client).walk(0xc0001)
	/build/internal/plugins/observium/client.go:214 +0x1a4`)
	r.CapturePanic(
		"observium: could not reach https://obs.acme-hospital.internal:8080/api/v0/devices?token=hnvKb3ygMNubJ24SYHaEWlUv4pVA",
		stack, "mcp.request")
	r.Flush(5 * time.Second)

	sent := c.transmitted()
	if sent == "" {
		t.Fatal("nothing reached the collector")
	}

	for _, leak := range []string{
		"acme-hospital", "obs.acme-hospital.internal",
		"hnvKb3ygMNubJ24SYHaEWlUv4pVA", "192.168",
	} {
		if strings.Contains(sent, leak) {
			t.Errorf("%q was transmitted:\n%s", leak, sent)
		}
	}
	// What makes it worth sending at all.
	for _, keep := range []string{
		"could not reach", "mcp.request", "v1.2.3",
		"internal/plugins/observium/client.go", "walk",
	} {
		if !strings.Contains(sent, keep) {
			t.Errorf("%q did not reach the collector, and it is what makes the "+
				"report actionable:\n%s", keep, sent)
		}
	}
}

// The machine's own name is often the most identifying thing on a customer
// deployment, and the SDK fills it in by default.
func TestEndToEnd_HostnameNeverLeaves(t *testing.T) {
	c := newCollector(t)
	r, _ := NewErrorReporter(ErrorReporterOptions{
		DSN: c.dsn(), IncludeMessages: true, Synchronous: true, Log: quietLog(),
	})
	if r == nil {
		t.Fatal("no reporter")
	}
	r.CapturePanic("something broke", nil, "test")
	r.Flush(5 * time.Second)

	sent := c.transmitted()
	// Checked precisely rather than by substring: a short hostname like "dev"
	// appears inside unrelated words such as the module version "(devel)", and
	// a test that cries leak over that gets ignored.
	host := hostnameForDiagnostics()
	if host != "" && host != "this machine" {
		if strings.Contains(sent, "\"server_name\":\""+host+"\"") {
			t.Errorf("the machine's hostname %q was sent as server_name:\n%s", host, sent)
		}
	}
	// The sentinel is what "not configured" has to look like on the wire,
	// because an empty string makes the SDK substitute the hostname.
	if !strings.Contains(sent, "\"server_name\":\" \"") {
		t.Errorf("server_name was not the withheld sentinel:\n%s", sent)
	}
	// The build machine's filesystem layout carries a build user's home
	// directory and says nothing the repository-relative filename does not.
	if strings.Contains(sent, "\"abs_path\"") {
		t.Errorf("a frame carried an absolute build path:\n%s", sent)
	}
}

// A label an operator chose does travel, because they chose it.
func TestEndToEnd_ChosenLabelIsSent(t *testing.T) {
	c := newCollector(t)
	r, _ := NewErrorReporter(ErrorReporterOptions{
		DSN: c.dsn(), InstanceLabel: "site-42", Synchronous: true, Log: quietLog(),
	})
	if r == nil {
		t.Fatal("no reporter")
	}
	r.CapturePanic("boom", nil, "test")
	r.Flush(5 * time.Second)

	if !strings.Contains(c.transmitted(), "site-42") {
		t.Errorf("the chosen label did not reach the collector:\n%s", c.transmitted())
	}
}

// With messages off the stack still travels and the sentences do not, which is
// the setting's whole purpose.
func TestEndToEnd_MessagesWithheld(t *testing.T) {
	c := newCollector(t)
	r, _ := NewErrorReporter(ErrorReporterOptions{
		DSN: c.dsn(), IncludeMessages: false, Synchronous: true, Log: quietLog(),
	})
	if r == nil {
		t.Fatal("no reporter")
	}
	r.CapturePanic("no device core-sw-01.dc2.acme.internal", nil, "mcp.request")
	r.Flush(5 * time.Second)

	sent := c.transmitted()
	for _, leak := range []string{"core-sw-01", "acme"} {
		if strings.Contains(sent, leak) {
			t.Errorf("%q was transmitted with messages off:\n%s", leak, sent)
		}
	}
	if !strings.Contains(sent, "mcp.request") {
		t.Error("the component tag was lost, and it is not estate data")
	}
}

// The default transport queues and drops rather than blocking, and Flush
// reports on the batch rather than on one event -- so the ordinary path can
// return success having delivered nothing. That is the right trade for a crash
// nobody is waiting on: a monitoring host must not stall because a collector
// is slow.
//
// It is the wrong trade anywhere an answer has to be trustworthy, which is why
// these tests and TestEvent ask for synchronous delivery. Before they did, the
// suite failed roughly one run in a hundred with nothing transmitted and Flush
// having returned true.
func TestEndToEnd_SynchronousDeliveryIsDeterministic(t *testing.T) {
	for i := 0; i < 50; i++ {
		c := newCollector(t)
		r, err := NewErrorReporter(ErrorReporterOptions{
			DSN: c.dsn(), Synchronous: true, Log: quietLog(),
		})
		if err != nil || r == nil {
			t.Fatalf("run %d: %v", i, err)
		}
		r.CapturePanic("boom", nil, "test")
		if c.transmitted() == "" {
			t.Fatalf("run %d: nothing was transmitted, and a synchronous send "+
				"has already returned by the time CapturePanic does", i)
		}
	}
}

// An operator who clicks "send a test event" is owed a real answer, so this
// path does not use the lossy transport either.
func TestTestEvent_ActuallyReachesTheCollector(t *testing.T) {
	c := newCollector(t)
	r, err := NewErrorReporter(ErrorReporterOptions{DSN: c.dsn(), Log: quietLog()})
	if err != nil || r == nil {
		t.Fatalf("NewErrorReporter: %v", err)
	}

	if err := r.TestEvent(); err != nil {
		t.Fatalf("TestEvent: %v", err)
	}
	if !strings.Contains(c.transmitted(), "test event") {
		t.Errorf("TestEvent reported success but nothing arrived:\n%s", c.transmitted())
	}
}

// Off means off, and saying so beats a silent no-op an operator reads as
// working.
func TestTestEvent_SaysSoWhenReportingIsOff(t *testing.T) {
	var r *ErrorReporter
	if err := r.TestEvent(); err == nil {
		t.Fatal("a disabled reporter reported a successful test event")
	}
}
