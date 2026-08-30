package notify

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// receiver records what arrived.
type receiver struct {
	mu     sync.Mutex
	bodies []string
	auth   string
	status int
}

func (r *receiver) serve() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.bodies = append(r.bodies, string(body))
		r.auth = req.Header.Get("Authorization")
		r.mu.Unlock()
		if r.status != 0 {
			w.WriteHeader(r.status)
		}
	}))
}

func (r *receiver) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.bodies...)
}

func fixed(c Config) func(context.Context) Config {
	return func(context.Context) Config { return c }
}

var event = Event{
	Kind: "approvals.bypass_opened", Severity: SeverityWarning,
	Title: "Changes are being approved without asking anyone",
	Text:  "user:someone opened a window until 14:00 UTC",
}

// Each receiver wants a different shape, and a payload that renders in one
// fails to render at all in another.
func TestEncodesForEachReceiver(t *testing.T) {
	t.Run("slack takes one text field", func(t *testing.T) {
		body, err := encode(Config{Format: FormatSlack}, event)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]string
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out["text"], "without asking anyone") {
			t.Errorf("text = %q", out["text"])
		}
		if len(out) != 1 {
			t.Errorf("slack payload carries %d fields; blocks render in one client and not another", len(out))
		}
	})

	t.Run("ntfy carries the topic in the body", func(t *testing.T) {
		body, err := encode(Config{Format: FormatNtfy, Topic: "mcpd-alerts"}, event)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
		if out["topic"] != "mcpd-alerts" {
			t.Errorf("topic = %v", out["topic"])
		}
		if out["priority"] != float64(4) {
			t.Errorf("priority = %v; a warning should be raised, not urgent", out["priority"])
		}
	})

	t.Run("json carries the kind a receiver routes on", func(t *testing.T) {
		body, err := encode(Config{Format: FormatJSON}, event)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
		if out["kind"] != "approvals.bypass_opened" {
			t.Errorf("kind = %v", out["kind"])
		}
		if out["source"] != "mcpd" {
			t.Errorf("source = %v", out["source"])
		}
	})
}

// Off until an address is set, like crash reporting: a host that started
// talking to an outside service because it was upgraded would be doing
// something nobody agreed to.
func TestSendsNothingWithoutAnAddress(t *testing.T) {
	r := &receiver{}
	server := r.serve()
	defer server.Close()

	n := New(quiet(), fixed(Config{}))
	n.Notify(context.Background(), event)

	if got := len(n.queue); got != 0 {
		t.Fatalf("queued %d events with nowhere to send them", got)
	}
}

func TestDelivers(t *testing.T) {
	r := &receiver{}
	server := r.serve()
	defer server.Close()

	n := New(quiet(), fixed(Config{URL: server.URL, Format: FormatJSON, Token: "s3cret"}))
	if err := n.Send(context.Background(), event); err != nil {
		t.Fatal(err)
	}

	got := r.got()
	if len(got) != 1 {
		t.Fatalf("received %d bodies, want 1", len(got))
	}
	if !strings.Contains(got[0], "bypass_opened") {
		t.Errorf("body = %s", got[0])
	}
	if r.auth != "Bearer s3cret" {
		t.Errorf("authorization = %q", r.auth)
	}
}

// A receiver's error page can carry anything, and this host has no business
// copying it anywhere.
func TestReportsAStatusWithoutTheBody(t *testing.T) {
	r := &receiver{status: http.StatusForbidden}
	server := r.serve()
	defer server.Close()

	n := New(quiet(), fixed(Config{URL: server.URL, Format: FormatJSON}))
	err := n.Send(context.Background(), event)
	if err == nil {
		t.Fatal("a refused delivery reported success")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error = %v, want the status", err)
	}
}

// A notification is a courtesy. Nothing this host does may fail, or wait,
// because one could not be delivered.
func TestNotifyNeverBlocks(t *testing.T) {
	// A receiver that never answers.
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		time.Sleep(time.Hour)
	}))
	defer server.Close()

	n := New(quiet(), fixed(Config{URL: server.URL, Format: FormatJSON}))

	done := make(chan struct{})
	go func() {
		// Far more than the queue holds, so this would block if the bound
		// were not enforced.
		for i := 0; i < queueDepth*4; i++ {
			n.Notify(context.Background(), event)
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Notify blocked on a receiver that never answers")
	}

	n.mu.Lock()
	dropped := n.dropped
	n.mu.Unlock()
	if dropped == 0 {
		t.Error("the queue grew without bound instead of dropping")
	}
}

func TestRunStopsWithItsContext(t *testing.T) {
	n := New(quiet(), fixed(Config{}))
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- n.Run(ctx) }()
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop when its context ended")
	}
}
