package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spoked/mcpd/internal/auth"
)

func testPrincipal() auth.Principal {
	return auth.Principal{
		ID: "svc:chatgpt", Role: auth.RoleUser, Plugins: []string{"echo"},
	}
}

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// A typo in a tunnel ID should be a startup error, not an opaque 401 from the
// control plane an hour later.
func TestConfig_Validate(t *testing.T) {
	const validID = "tunnel_0123456789abcdef0123456789abcdef"

	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{"valid", Config{TunnelID: validID, APIKey: "k", Principal: testPrincipal()}, ""},
		{"no id", Config{APIKey: "k", Principal: testPrincipal()}, "tunnel_"},
		{"wrong prefix", Config{TunnelID: "tun_0123456789abcdef0123456789abcdef",
			APIKey: "k", Principal: testPrincipal()}, "tunnel_"},
		{"too short", Config{TunnelID: "tunnel_abc", APIKey: "k",
			Principal: testPrincipal()}, "tunnel_"},
		{"uppercase hex", Config{TunnelID: "tunnel_0123456789ABCDEF0123456789abcdef",
			APIKey: "k", Principal: testPrincipal()}, "tunnel_"},
		{"no key", Config{TunnelID: validID, Principal: testPrincipal()}, "API key"},
		{"no plugin grants", Config{TunnelID: validID, APIKey: "k",
			Principal: auth.Principal{ID: "svc:x", Role: auth.RoleUser}}, "plugin"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("should be valid: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected a validation error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestManager_DisabledWithoutATunnelID(t *testing.T) {
	m := NewManager(Config{}, nil, discardLog())

	if m.Enabled() {
		t.Fatal("a manager with no tunnel id must report disabled")
	}
	if got := m.Status().State; got != StateDisabled {
		t.Fatalf("state = %s, want disabled", got)
	}
	if err := m.Start(context.Background()); err == nil {
		t.Fatal("starting an unconfigured tunnel must fail")
	}
}

// The status is rendered on a dashboard, so it must carry enough to see what
// the tunnel can do and nothing that would leak the credential.
func TestManager_StatusShowsGrantsButNeverTheKey(t *testing.T) {
	const key = "sk-super-secret-runtime-key"
	m := NewManager(Config{
		TunnelID:  "tunnel_0123456789abcdef0123456789abcdef",
		APIKey:    key,
		Principal: testPrincipal(),
	}, nil, discardLog())

	s := m.Status()
	if s.TunnelID == "" || s.Principal != "svc:chatgpt" {
		t.Fatalf("status should identify the tunnel and its principal: %+v", s)
	}
	if len(s.Plugins) != 1 || s.Plugins[0] != "echo" {
		t.Fatalf("status should show what the tunnel can reach: %+v", s.Plugins)
	}
	if strings.Contains(s.Message+s.TunnelID+s.Principal+s.Role, key) {
		t.Fatal("the API key must never appear in status")
	}
}

// A failed start must not leave the credential in a message that reaches logs
// or the dashboard.
func TestManager_FailureRedactsTheKey(t *testing.T) {
	const key = "sk-super-secret-runtime-key"

	// A control plane that refuses, so Start fails for a real reason.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad key ` + key + `"}`))
	}))
	defer srv.Close()

	m := NewManager(Config{
		TunnelID:            "tunnel_0123456789abcdef0123456789abcdef",
		APIKey:              key,
		Principal:           testPrincipal(),
		ControlPlaneBaseURL: srv.URL,
	}, func(*auth.Principal) (*mcp.Server, error) {
		return mcp.NewServer(&mcp.Implementation{Name: "t", Version: "1"}, nil), nil
	}, discardLog())

	err := m.Start(context.Background())
	defer m.Stop(context.Background())

	// Start no longer blocks on readiness, so it may succeed here and fail
	// asynchronously. Either way, nothing may carry the key.
	if err != nil && strings.Contains(err.Error(), key) {
		t.Fatalf("the error leaked the API key: %v", err)
	}
	if strings.Contains(m.Status().Message, key) {
		t.Fatal("the status message leaked the API key")
	}
}

// A healthy but idle tunnel is slow to report ready, because the client only
// does so after a completed control-plane poll and an empty poll waits out its
// timeout. Blocking on that tore down working tunnels.
func TestManager_StartDoesNotBlockOnReadiness(t *testing.T) {
	// A control plane that accepts but never returns from a poll.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
	}))
	defer srv.Close()

	m := NewManager(Config{
		TunnelID:            "tunnel_0123456789abcdef0123456789abcdef",
		APIKey:              "sk-runtime",
		Principal:           testPrincipal(),
		ControlPlaneBaseURL: srv.URL,
	}, func(*auth.Principal) (*mcp.Server, error) {
		return mcp.NewServer(&mcp.Implementation{Name: "t", Version: "1"}, nil), nil
	}, discardLog())

	done := make(chan error, 1)
	go func() { done <- m.Start(context.Background()) }()

	select {
	case <-done:
		// Returned promptly, which is the point.
	case <-time.After(10 * time.Second):
		t.Fatal("Start blocked waiting for readiness; an idle tunnel would be " +
			"torn down despite working")
	}

	if state := m.Status().State; state == StateFailed {
		t.Fatal("a tunnel that has not yet completed its first poll must not " +
			"be reported as failed")
	}
	_ = m.Stop(context.Background())
}

// A server factory that refuses -- because the principal is granted nothing
// mounted, say -- must fail the start rather than connecting an empty tunnel.
func TestManager_ServerFactoryFailureStopsTheStart(t *testing.T) {
	m := NewManager(Config{
		TunnelID:  "tunnel_0123456789abcdef0123456789abcdef",
		APIKey:    "k",
		Principal: testPrincipal(),
	}, func(*auth.Principal) (*mcp.Server, error) {
		return nil, errors.New("granted no mounted plugins")
	}, discardLog())

	if err := m.Start(context.Background()); err == nil {
		t.Fatal("a failing server factory must stop the start")
	}
	if m.Status().State != StateFailed {
		t.Fatalf("state = %s, want failed", m.Status().State)
	}
}

func TestManager_StopIsSafeWhenNotRunning(t *testing.T) {
	m := NewManager(Config{}, nil, discardLog())
	if err := m.Stop(context.Background()); err != nil {
		t.Fatalf("stopping a tunnel that never started should be a no-op: %v", err)
	}
}

func TestRedactKey(t *testing.T) {
	const key = "sk-abc123"
	err := errors.New("request failed with " + key + " rejected")

	got := redactKey(err, key)
	if strings.Contains(got.Error(), key) {
		t.Fatalf("redactKey left the key in: %v", got)
	}
	if !strings.Contains(got.Error(), "[REDACTED]") {
		t.Fatalf("expected a redaction marker: %v", got)
	}
	if redactKey(nil, key) != nil {
		t.Fatal("redacting nil should stay nil")
	}
}

// The embedded version comes from build info, so it cannot drift from what is
// actually linked.
func TestEmbeddedVersion(t *testing.T) {
	got := EmbeddedVersion()
	if got == "" {
		t.Fatal("embedded version must not be empty")
	}
	// In a test binary the dependency is present, so this should resolve.
	if got == "unknown" {
		t.Log("build info did not report the tunnel client; acceptable in some builds")
	}
}

func TestNormalizeVersion(t *testing.T) {
	tests := []struct{ in, want string }{
		{"v0.0.12", "0.0.12"},
		{"0.0.12", "0.0.12"},
		{"  v1.2.3  ", "1.2.3"},
	}
	for _, tc := range tests {
		if got := normalizeVersion(tc.in); got != tc.want {
			t.Errorf("normalizeVersion(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A release check that cannot complete is ordinary -- an air-gapped host, a
// rate limit -- and must never affect startup.
func TestCheckLatest_FailureIsNotFatal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	info := CheckLatest(context.Background(), srv.Client(), srv.URL, discardLog())
	if info.Embedded == "" {
		t.Fatal("the embedded version should still be reported")
	}
	if info.UpdateAvailable {
		t.Fatal("a failed check must not claim an update is available")
	}
}

func TestChecker_ReportsEmbeddedVersionBeforeAnyCheck(t *testing.T) {
	c := NewChecker(http.DefaultClient, discardLog(), 0)
	if c.Info().Embedded == "" {
		t.Fatal("a checker should report the embedded version immediately")
	}
}

// The version check must report an update when one exists, and must never
// install anything: the client is compiled in, so the only honest outcome is
// telling the operator to rebuild.
func TestCheckLatest_ReportsButDoesNotInstall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v99.0.0","draft":false,"prerelease":false}`))
	}))
	defer srv.Close()

	info := CheckLatest(context.Background(), srv.Client(), srv.URL, discardLog())
	if info.Latest != "v99.0.0" {
		t.Fatalf("latest = %q, want v99.0.0", info.Latest)
	}
	if !info.UpdateAvailable {
		t.Fatal("a newer release should be reported as available")
	}
	if !strings.Contains(info.Note, "rebuild") {
		t.Fatalf("the note must say what to do about it, got %q", info.Note)
	}
	if info.CheckedAt == nil {
		t.Fatal("a successful check should stamp when it ran")
	}
}

// A draft or prerelease is not something to tell an operator to rebuild for.
func TestCheckLatest_IgnoresDraftsAndPrereleases(t *testing.T) {
	for _, body := range []string{
		`{"tag_name":"v99.0.0","draft":true}`,
		`{"tag_name":"v99.0.0","prerelease":true}`,
		`{"tag_name":""}`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		info := CheckLatest(context.Background(), srv.Client(), srv.URL, discardLog())
		srv.Close()

		if info.UpdateAvailable {
			t.Errorf("%s should not be reported as an update", body)
		}
	}
}

// The embedded version matching the latest release is the normal case and
// must not nag.
func TestCheckLatest_NoUpdateWhenCurrent(t *testing.T) {
	embedded := EmbeddedVersion()
	if embedded == "unknown" {
		t.Skip("build info unavailable")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"` + embedded + `"}`))
	}))
	defer srv.Close()

	info := CheckLatest(context.Background(), srv.Client(), srv.URL, discardLog())
	if info.UpdateAvailable {
		t.Fatalf("embedded %s matches latest %s but an update was reported",
			info.Embedded, info.Latest)
	}
	if info.Note != "" {
		t.Fatalf("no note should be produced when current: %q", info.Note)
	}
}

// Two settings changes arriving together must not interleave.
//
// The dashboard's settings watcher spawns a goroutine per change, so two saves
// in quick succession run two Reconfigures against the same manager at the
// same time. Each does stop, replace, then start — and before the ops lock
// existed nothing serialised those three, so one reconfiguration's Start could
// run against the other's configuration.
//
// It surfaced as a data race rather than as a wrong tunnel: Start read m.cfg
// after releasing the field lock, including from two goroutines that outlive
// it, while Reconfigure wrote it. CI caught it under -race on a run where an
// unrelated change had made the server factory slow enough to widen the
// window. That is the shape reproduced here — Enabled is set, because
// Reconfigure returns before ever reaching Start without it, and Start is
// where the racing read lives.
func TestReconfigure_DoesNotInterleaveWithAnother(t *testing.T) {
	// A control plane that accepts the connection and then says nothing, so
	// the tunnel gets far enough to leave its goroutines behind — which are
	// the ones whose reads of the configuration raced.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()

	// Slow enough that two reconfigurations genuinely overlap, which is the
	// condition CI hit by accident when a fifth plugin made building the
	// server slower. It reads nothing, so anything the detector reports is the
	// manager's own doing.
	slow := func(*auth.Principal) (*mcp.Server, error) {
		time.Sleep(2 * time.Millisecond)
		return mcp.NewServer(&mcp.Implementation{Name: "t", Version: "1"}, nil), nil
	}

	const (
		idA = "tunnel_0123456789abcdef0123456789abcdef"
		idB = "tunnel_fedcba9876543210fedcba9876543210"
	)
	config := func(i int) Config {
		c := Config{
			Enabled: true, Plugin: "echo", TunnelID: idA, APIKey: "sk-a",
			Principal:           auth.Principal{ID: "svc:a", Role: auth.RoleUser, Plugins: []string{"echo"}, TokenID: "a"},
			ControlPlaneBaseURL: srv.URL,
		}
		if i%2 == 1 {
			c.TunnelID, c.APIKey = idB, "sk-b"
			c.Principal = auth.Principal{ID: "svc:b", Role: auth.RoleUser, Plugins: []string{"echo"}, TokenID: "b"}
		}
		return c
	}

	m := NewManager(config(0), slow, discardLog())
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := range 6 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Not asserted on: against a control plane that answers nothing
			// these may fail, and the failure is not what this is about. What
			// must hold is that the race detector stays quiet.
			_ = m.Reconfigure(ctx, config(i))
		}(i)
	}
	wg.Wait()

	// Whichever reconfiguration won, the manager holds one whole configuration
	// rather than a mixture. A tunnel id from one save beside a key from
	// another would authenticate as the wrong ChatGPT workspace.
	got := m.Config()
	switch got.TunnelID {
	case idA:
		if got.APIKey != "sk-a" || got.Principal.ID != "svc:a" {
			t.Errorf("mixed configuration: tunnel %s with key %q and principal %q",
				got.TunnelID, got.APIKey, got.Principal.ID)
		}
	case idB:
		if got.APIKey != "sk-b" || got.Principal.ID != "svc:b" {
			t.Errorf("mixed configuration: tunnel %s with key %q and principal %q",
				got.TunnelID, got.APIKey, got.Principal.ID)
		}
	default:
		t.Errorf("the manager ended on a tunnel id nobody configured: %q", got.TunnelID)
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = m.Stop(stopCtx)
}

// A tunnel that stops does not restart itself, and the container's healthcheck
// validates configuration rather than the connection -- so this callback is
// the only thing that reaches a person. That gap is what let the tunnels sit
// dead from midnight until somebody tried to use them the next morning.
//
// It fires once per failure rather than once per report of one: the
// rejected-credential path calls fail twice deliberately, because Stop resets
// the state and would otherwise erase the explanation an operator needs.
func TestFailReportsOncePerFailure(t *testing.T) {
	var got []string
	m := NewManager(Config{Plugin: "graylog", TunnelID: "tunnel_abc"}, nil, discardLogger())
	m.onFailure = func(plugin, tunnelID, reason string, _ bool) {
		got = append(got, plugin+"|"+tunnelID+"|"+reason)
	}

	m.fail(errors.New("the control plane rejected the key"), false)
	m.fail(errors.New("the control plane rejected the key"), false)

	if len(got) != 1 {
		t.Fatalf("want one report, got %d: %v", len(got), got)
	}
	if want := "graylog|tunnel_abc|the control plane rejected the key"; got[0] != want {
		t.Errorf("reported %q, want %q", got[0], want)
	}
	if s := m.Status(); s.State != StateFailed {
		t.Errorf("state = %s, want %s", s.State, StateFailed)
	}
}

// A group hands its callback to every tunnel it builds, so one set at the
// composition root covers tunnels added later too.
func TestGroupPassesTheFailureHookToItsManagers(t *testing.T) {
	var called int
	g := NewGroup(discardLogger())
	g.OnFailure = func(string, string, string, bool) { called++ }

	if err := g.Apply(t.Context(), []Config{
		{Plugin: "graylog", TunnelID: "tunnel_abc", APIKey: "k",
			Principal: testConfig().Principal},
	}, testFactory()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	m := g.Lookup("graylog")
	if m == nil {
		t.Fatal("the tunnel was not built")
	}
	m.fail(errors.New("boom"), false)
	if called != 1 {
		t.Fatalf("the group's hook was not given to the manager: called %d times", called)
	}
}

// A failure worth retrying schedules the next attempt with a growing delay,
// and a failure that is not -- a rejected credential, a configuration that
// cannot start -- schedules nothing, because nothing about it will be
// different in two seconds. The dashboard tells the two apart by whether a
// next attempt is due.
func TestSupervisor_RetriesOnlyWhatCouldWork(t *testing.T) {
	m := NewManager(Config{Enabled: true, Plugin: "graylog", TunnelID: "tunnel_abc"}, nil, discardLogger())
	// Long enough that the timer cannot fire during the test.
	restore := retryBase
	retryBase = time.Hour
	t.Cleanup(func() { retryBase = restore })

	m.fail(errors.New("the control plane could not be reached"), true)
	s := m.Status()
	if s.Attempts != 1 || s.NextRetryAt == nil {
		t.Fatalf("a retryable failure should schedule attempt 1: %+v", s)
	}

	// A second failure while one is already scheduled does not stack a
	// second timer.
	m.fail(errors.New("still unreachable"), true)
	if s := m.Status(); s.Attempts != 1 {
		t.Fatalf("attempts = %d, want the pending one only", s.Attempts)
	}

	// Stopping is a decision, and cancels what was due.
	if err := m.Stop(t.Context()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if s := m.Status(); s.NextRetryAt != nil || s.Attempts != 0 {
		t.Fatalf("stop should cancel the retry: %+v", s)
	}

	final := NewManager(Config{Enabled: true, Plugin: "graylog", TunnelID: "tunnel_abc"}, nil, discardLogger())
	final.fail(errors.New("OpenAI did not recognise that key"), false)
	if s := final.Status(); s.NextRetryAt != nil || s.State != StateFailed {
		t.Fatalf("a final failure must not be retried: %+v", s)
	}
}

func TestBackoff_DoublesToACap(t *testing.T) {
	restoreBase, restoreCap := retryBase, retryCap
	retryBase, retryCap = time.Second, 10*time.Second
	t.Cleanup(func() { retryBase, retryCap = restoreBase, restoreCap })
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 10 * time.Second, 10 * time.Second}
	for i, w := range want {
		if got := backoff(i + 1); got != w {
			t.Errorf("backoff(%d) = %s, want %s", i+1, got, w)
		}
	}
}

// The person told a connector had stopped is told again when retrying has
// stopped being reassuring, and told it is over when it comes back.
func TestSupervisor_SaysWhenRetryingHasNotWorked(t *testing.T) {
	restore := retryBase
	retryBase = time.Hour
	t.Cleanup(func() { retryBase = restore })
	var reports []string
	m := NewManager(Config{Enabled: true, Plugin: "graylog", TunnelID: "tunnel_abc"}, nil, discardLogger())
	m.onFailure = func(_, _, reason string, retrying bool) {
		reports = append(reports, fmt.Sprintf("%v:%s", retrying, reason))
	}
	for i := 0; i < stillDownAfter; i++ {
		m.fail(errors.New("unreachable"), true)
		// Each attempt is scheduled and, in the real thing, fires; here the
		// timer is cleared by hand so the next failure counts as an attempt.
		m.mu.Lock()
		if m.retry != nil {
			m.retry.Stop()
			m.retry = nil
		}
		m.mu.Unlock()
	}
	if len(reports) != 2 {
		t.Fatalf("want the first failure and the still-down report, got %v", reports)
	}
	if !strings.HasPrefix(reports[0], "true:") || !strings.Contains(reports[1], "still not connecting after") {
		t.Errorf("reports = %v", reports)
	}
}

// A tunnel is degraded when its client keeps reporting errors and nothing
// gets through; a served request clears it, and so do the errors stopping.
func TestLiveness_DegradedIsErrorsWithNothingServed(t *testing.T) {
	m := NewManager(Config{Enabled: true, Plugin: "graylog", TunnelID: "tunnel_abc"}, nil, discardLogger())
	clock := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return clock }
	m.mu.Lock()
	m.state = StateConnected
	m.mu.Unlock()

	m.noteTrouble("poll failed; backing off")
	if m.Status().Degraded {
		t.Fatal("one error is not degraded")
	}
	clock = clock.Add(degradedAfter)
	m.noteTrouble("poll failed; backing off")
	s := m.Status()
	if !s.Degraded || s.Trouble == "" || s.TroubleAt == nil {
		t.Fatalf("errors for %s with nothing served should be degraded: %+v", degradedAfter, s)
	}

	// Something got through, so the client's complaints were not stopping it.
	m.noteRequest()
	if s := m.Status(); s.Degraded || s.Requests != 1 || s.LastRequestAt == nil {
		t.Fatalf("a served request clears degraded: %+v", s)
	}

	// Errors that stopped on their own clear it too.
	m.noteTrouble("poll failed; backing off")
	clock = clock.Add(degradedAfter)
	m.noteTrouble("poll failed; backing off")
	if !m.Status().Degraded {
		t.Fatal("degraded again")
	}
	clock = clock.Add(troubleWindow + time.Minute)
	if m.Status().Degraded {
		t.Fatal("errors that stopped are not a degradation")
	}
}

// The control plane's refusal to let this key use this tunnel is as final as
// a bad key, and left alone it is one warning every ten seconds for ever.
func TestCredentialRejection_KnowsAForbiddenTunnel(t *testing.T) {
	line := `level=WARN msg="poll failed; backing off" error_code=tunnel_use_forbidden retry_in_ms=10000`
	if got := credentialRejection(line); got != "tunnel_use_forbidden" {
		t.Fatalf("credentialRejection = %q", got)
	}
	if !strings.Contains(diagnose("sk-proj-x", "tunnel_use_forbidden"), "organisation") {
		t.Error("the diagnosis should say the tunnel is in another organisation")
	}
	if !pollFailure(line) {
		t.Error("a poll backing off is trouble, whatever level the client filed it under")
	}
	if pollFailure(`level=INFO msg="tunnel connected"`) {
		t.Error("an ordinary line is not")
	}
}

type fakeUpstream struct {
	present map[string]bool
	err     error
}

func (f fakeUpstream) Exists(_ context.Context, id string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.present[id], nil
}

// A tunnel deleted in OpenAI's dashboard is never told; the check is what
// says so. A check that could not run leaves the last answer standing rather
// than calling the tunnel missing.
func TestGroupCheckUpstream_RecordsWhatOpenAISaid(t *testing.T) {
	g := NewGroup(discardLogger())
	cfgs := []Config{
		{Plugin: "graylog", TunnelID: "tunnel_abc", APIKey: "k", AccountID: "a", Principal: testConfig().Principal},
		{Plugin: "echo", TunnelID: "tunnel_def", APIKey: "k", AccountID: "a", Principal: testConfig().Principal},
	}
	if err := g.Apply(t.Context(), cfgs, testFactory()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	g.CheckUpstream(t.Context(), func(string) UpstreamChecker {
		return fakeUpstream{present: map[string]bool{"tunnel_abc": true}}
	})
	st := g.Status()
	if st[0].Upstream != "present" || st[1].Upstream != "missing" {
		t.Fatalf("upstream = %q, %q", st[0].Upstream, st[1].Upstream)
	}

	g.CheckUpstream(t.Context(), func(string) UpstreamChecker {
		return fakeUpstream{err: errors.New("admin key expired")}
	})
	if st := g.Status(); st[1].Upstream != "missing" || st[0].Upstream != "present" {
		t.Fatalf("a check that could not run must leave the answer alone: %+v", st)
	}

	// No admin key for the account: nothing is asked, nothing is said.
	h := NewGroup(discardLogger())
	if err := h.Apply(t.Context(), cfgs[:1], testFactory()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	h.CheckUpstream(t.Context(), func(string) UpstreamChecker { return nil })
	if st := h.Status(); st[0].Upstream != "" {
		t.Fatalf("unchecked should stay unchecked: %q", st[0].Upstream)
	}
}

func TestGroupRestart_NeedsATunnelItKnows(t *testing.T) {
	g := NewGroup(discardLogger())
	g.Factory = testFactory()
	if err := g.Restart(t.Context(), "tunnel_nope"); err == nil {
		t.Fatal("restarting a tunnel that is not configured should refuse")
	}
}

// The request history is by the hour, oldest first, with idle hours zeroed
// rather than left holding a stale count -- and it outlives a rebuild, so a
// restarted connector does not read as one that was never used.
func TestActivity_HourlyRingAndInheritance(t *testing.T) {
	m := NewManager(Config{Enabled: true, Plugin: "graylog", TunnelID: "tunnel_abc"}, nil, discardLogger())
	clock := time.Date(2026, 9, 2, 10, 30, 0, 0, time.UTC)
	m.now = func() time.Time { return clock }

	m.noteRequest()
	m.noteRequest()
	clock = clock.Add(time.Hour)
	m.noteRequest()
	// Two idle hours, then one more.
	clock = clock.Add(3 * time.Hour)
	m.noteRequest()

	got := m.Status().Activity
	if len(got) != activityHours {
		t.Fatalf("len = %d, want %d", len(got), activityHours)
	}
	tail := got[activityHours-5:]
	want := []int64{2, 1, 0, 0, 1}
	for i := range want {
		if tail[i] != want[i] {
			t.Fatalf("last five hours = %v, want %v", tail, want)
		}
	}

	next := NewManager(m.Config(), nil, discardLogger())
	next.now = m.now
	next.Inherit(m)
	if s := next.Status(); s.Activity[activityHours-1] != 1 || s.LastRequestAt == nil {
		t.Fatalf("the replacement should carry the history: %+v", s)
	}
}

// A rejected key is reported once. It used to be recorded, stopped, and
// recorded again -- stopping had erased the state and the second record was
// what put the explanation back -- and every rejected key reached Discord
// twice.
func TestRejectedKey_IsReportedOnce(t *testing.T) {
	var reports []string
	m := NewManager(Config{Enabled: true, Plugin: "graylog", TunnelID: "tunnel_abc"}, nil, discardLogger())
	m.onFailure = func(_, _, reason string, _ bool) { reports = append(reports, reason) }

	m.fail(errors.New("tunnel: OpenAI rejected the key"), false)
	if err := m.halt(t.Context()); err != nil {
		t.Fatalf("halt: %v", err)
	}
	s := m.Status()
	if s.State != StateFailed || !strings.Contains(s.Message, "rejected") {
		t.Fatalf("halting must keep the failure and its reason: %+v", s)
	}
	// Nothing else to say: the failure is already recorded and reported.
	if len(reports) != 1 {
		t.Fatalf("want one report, got %d: %v", len(reports), reports)
	}
}
