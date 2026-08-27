package graylog

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/plugins"
)

// An unconfigured plugin mounts. Its settings form has to have somewhere to
// live, and Check has to be able to say what is missing -- which is the whole
// path somebody follows to fix it.
func TestUnconfigured_MountsAndSaysWhatIsMissing(t *testing.T) {
	p, err := New(plugins.Deps{
		Instance: "graylog",
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:      time.Now,
	}, Config{})
	if err != nil {
		t.Fatalf("New with no settings should not fail: %v", err)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start with no settings should not fail: %v", err)
	}

	health := p.Check(context.Background())
	if health.State == plugins.HealthyState {
		t.Error("an unconfigured plugin reported itself healthy")
	}
	if !strings.Contains(health.Message, "access token") {
		t.Errorf("the health message should say what to set: %q", health.Message)
	}

	// And a tool call says so plainly rather than failing at the network. A
	// model told "not configured yet" stops and says so; one handed a
	// connection error tries three more tools first.
	_, err = p.searchMessages(context.Background(), searchArgs{})
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Errorf("search on an unconfigured plugin: %v", err)
	}
}

// The tool list is a context cost paid on every conversation rather than only
// when a tool is called, so what is on it is a decision. This pins the set:
// adding one should be deliberate, and so should the fact that none of them
// writes.
//
// The verb_resource scheme these follow is enforced centrally, in
// plugins.checkToolName, rather than checked again here -- a rule written
// twice becomes two rules. What this pins is the set.
func TestRegister_DeclaresSevenReadToolsAndNoMutations(t *testing.T) {
	p := toolPlugin(t, jsonOK(`{}`))

	m := plugins.NewManager(slog.New(slog.NewTextHandler(io.Discard, nil)), "test",
		func(context.Context, string, auth.Capability) error { return nil }, nil, nil, nil)
	if err := m.Register(context.Background(), p, "graylog", false); err != nil {
		t.Fatalf("Register: %v", err)
	}
	registry := m.Lookup("graylog").Registry

	names := registry.ToolNames()
	sort.Strings(names)
	want := []string{
		"graylog_aggregate_messages",
		"graylog_get_system_status",
		"graylog_list_event_definitions",
		"graylog_list_message_fields",
		"graylog_list_streams",
		"graylog_search_events",
		"graylog_search_messages",
	}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("tools = %v, want %v", names, want)
	}
	// Read-only is a property of the whole integration, not only of the
	// transport. A mutation registered here would be an approval-gated write,
	// and there is deliberately no such thing.
	if got := registry.MutationActions(); len(got) != 0 {
		t.Errorf("mutations = %v; this integration is read-only", got)
	}
}

// The type declaration is what the dashboard renders, validates and encrypts.
// A mistake in it is a developer's, so it is caught here rather than by an
// operator finding a field they cannot fill in.
func TestType_IsValid(t *testing.T) {
	if err := Type().Validate(); err != nil {
		t.Fatalf("Type: %v", err)
	}
	if err := (&Plugin{}).Descriptor().Validate(); err != nil {
		t.Fatalf("Descriptor: %v", err)
	}
}

// The credential is not kept on the config the plugin holds, so a dump of it
// -- a log line, an error, the settings page -- cannot carry one.
func TestNew_DoesNotRetainCredentials(t *testing.T) {
	p, err := New(plugins.Deps{
		Instance: "graylog",
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:      time.Now,
	}, Config{
		BaseURL: "https://graylog.example",
		Token:   "supersecret",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.cfg.Token != "" {
		t.Errorf("the plugin kept a credential on its config: %+v", p.cfg)
	}
}

// A wrong credential should be a message on the dashboard rather than a
// confusing failure inside the first tool call an assistant makes.
func TestStart_ReportsARejectedCredential(t *testing.T) {
	p := toolPlugin(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"type":"ApiError","message":"Invalid credentials"}`))
	})

	if err := p.Start(context.Background()); err == nil {
		t.Fatal("Start succeeded against a 401")
	}
	health := p.Check(context.Background())
	if health.State == plugins.HealthyState {
		t.Error("the plugin reported healthy after a failed probe")
	}
	if !strings.Contains(health.Message, "TTL") {
		t.Errorf("the message should mention that a token expires: %q", health.Message)
	}
}
