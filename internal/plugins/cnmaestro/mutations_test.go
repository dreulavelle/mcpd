package cnmaestro

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/operations"
	"github.com/spoked/mcpd/internal/plugins"
)

// fakeController stands in for cnMaestro, returning the response shapes the
// 6.3.0 specification defines.
type fakeController struct {
	mu sync.Mutex

	device    map[string]any
	stats     map[string]any
	putBodies []map[string]any
	putStatus int
	rebooted  int
	tokenHits int
}

func newFakeController() *fakeController {
	return &fakeController{
		device: map[string]any{
			"mac":      "AA:BB:CC:DD:EE:FF",
			"name":     "Lobby-East",
			"type":     "wifi-enterprise",
			"status":   "online",
			"network":  "Campus",
			"ap_group": "Campus-Indoor",
			"overrides": map[string]any{
				"radios": []any{
					map[string]any{"id": 1, "channel": "6", "power": "auto"},
					map[string]any{"id": 2, "channel": "36", "channel_width": 80},
				},
				// A setting this mutation must preserve untouched.
				"location": "Building A",
			},
		},
		stats: map[string]any{
			"mac": "AA:BB:CC:DD:EE:FF", "status": "online",
			"uptime": 864000, "connected_clients": 12,
		},
		putStatus: http.StatusOK,
	}
}

func (f *fakeController) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/v2/access/token", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.tokenHits++
		f.mu.Unlock()
		writeJSON(w, map[string]any{
			"access_token": "tok-abc", "token_type": "bearer", "expires_in": 3600,
		})
	})

	mux.HandleFunc("GET /api/v2/devices/{mac}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		writeJSON(w, map[string]any{
			"paging": map[string]any{"total": 1, "limit": 100},
			"data":   []any{f.device},
		})
	})

	mux.HandleFunc("GET /api/v2/devices/{mac}/statistics", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		writeJSON(w, map[string]any{
			"paging": map[string]any{"total": 1},
			"data":   []any{f.stats},
		})
	})

	mux.HandleFunc("PUT /api/v2/devices/{mac}", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)

		f.mu.Lock()
		f.putBodies = append(f.putBodies, body)
		status := f.putStatus
		if status == http.StatusOK {
			// Reflect the change so Observe sees it, the way the real
			// controller eventually would.
			if ov, ok := body["overrides"].(map[string]any); ok {
				f.device["overrides"] = ov
			}
		}
		f.mu.Unlock()

		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"message":"upstream failure"}`))
			return
		}
		writeJSON(w, map[string]any{"message": "Success"})
	})

	mux.HandleFunc("POST /api/v2/devices/{mac}/reboot", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.rebooted++
		f.stats["uptime"] = 5
		f.mu.Unlock()
		writeJSON(w, map[string]any{"message": "Success"})
	})

	mux.HandleFunc("GET /api/v2/devices", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		writeJSON(w, map[string]any{
			"paging": map[string]any{"total": 1, "limit": 1},
			"data":   []any{f.device},
		})
	})

	// Any path not registered above is a bug in the client, and a 404 with a
	// clear body makes that obvious in a test failure.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"unexpected path ` + r.URL.Path + `"}`))
	})
	return mux
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// fakeSecrets satisfies plugins.SecretSource.
type fakeSecrets struct{}

func (fakeSecrets) Secret(name string) (string, error) {
	switch name {
	case "client_id_ref":
		return "client-id", nil
	case "client_secret_ref":
		return "client-secret", nil
	}
	return "", errors.New("no such secret")
}

func newTestPlugin(t *testing.T, f *fakeController) (*Plugin, *httptest.Server) {
	t.Helper()
	srv := httptest.NewTLSServer(f.handler())
	t.Cleanup(srv.Close)

	p, err := New(plugins.Deps{
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Secrets: fakeSecrets{},
		HTTP:    srv.Client(),
		Now:     time.Now,
	}, Config{
		BaseURL:           srv.URL,
		ClientIDRef:       "client_id_ref",
		ClientSecretRef:   "client_secret_ref",
		ManagedAccount:    MainAccount,
		RequestsPerSecond: 1000,
		Burst:             1000,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p, srv
}

func TestSetRadioChannel_PlanDescribesTheChange(t *testing.T) {
	f := newFakeController()
	p, _ := newTestPlugin(t, f)
	h := &setRadioChannel{p: p}

	plan, err := h.Plan(context.Background(), SetRadioChannelParams{
		MAC: "AA:BB:CC:DD:EE:FF", Band: "5GHz", RadioID: 2, Channel: "149",
	})
	if err != nil {
		t.Fatal(err)
	}

	if plan.Before.Channel != "36" {
		t.Fatalf("before channel = %q, want 36", plan.Before.Channel)
	}
	if plan.Desired.Channel != "149" {
		t.Fatalf("desired channel = %q, want 149", plan.Desired.Channel)
	}
	// The ap_group must be captured: the API requires it on any overrides
	// change, and a group change between proposal and execution alters what
	// the change actually does.
	if plan.Before.APGroup != "Campus-Indoor" {
		t.Fatalf("ap_group = %q, want it captured in the plan", plan.Before.APGroup)
	}
	if plan.Preconditions["ap_group"] != "Campus-Indoor" {
		t.Fatal("ap_group must be part of the precondition snapshot")
	}
	if len(plan.Changes) == 0 || plan.Changes[0].Field != "channel" {
		t.Fatalf("changes = %+v, want a channel diff", plan.Changes)
	}
	if !strings.Contains(plan.Impact, "disconnect") {
		t.Fatalf("impact should tell an approver what happens to clients, got %q", plan.Impact)
	}
	// The rollback must restore the original channel.
	rb, ok := plan.Rollback.(SetRadioChannelParams)
	if !ok || rb.Channel != "36" {
		t.Fatalf("rollback = %+v, want it to restore channel 36", plan.Rollback)
	}
}

func TestSetRadioChannel_ValidatesParameters(t *testing.T) {
	f := newFakeController()
	p, _ := newTestPlugin(t, f)
	h := &setRadioChannel{p: p}

	tests := []struct {
		name   string
		params SetRadioChannelParams
		want   string
	}{
		{"bad mac", SetRadioChannelParams{MAC: "nope", Band: "5GHz", RadioID: 2, Channel: "149"}, "MAC address"},
		{"bad band", SetRadioChannelParams{MAC: "AA:BB:CC:DD:EE:FF", Band: "7GHz", RadioID: 1, Channel: "149"}, "band must be"},
		// Radio ids overlap across bands, so an id valid for one band is not
		// automatically valid for another.
		{"radio id wrong for band", SetRadioChannelParams{MAC: "AA:BB:CC:DD:EE:FF", Band: "2.4GHz", RadioID: 2, Channel: "6"}, "not valid for"},
		{"channel wrong for band", SetRadioChannelParams{MAC: "AA:BB:CC:DD:EE:FF", Band: "5GHz", RadioID: 2, Channel: "6"}, "not valid for"},
		{"channel as integer-ish nonsense", SetRadioChannelParams{MAC: "AA:BB:CC:DD:EE:FF", Band: "5GHz", RadioID: 2, Channel: "999"}, "not valid for"},
		{"bad width", SetRadioChannelParams{MAC: "AA:BB:CC:DD:EE:FF", Band: "5GHz", RadioID: 2, Channel: "149", Width: intPtr(33)}, "channel_width"},
		{"no-op change", SetRadioChannelParams{MAC: "AA:BB:CC:DD:EE:FF", Band: "5GHz", RadioID: 2, Channel: "36"}, "already configured"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := h.Plan(context.Background(), tc.params)
			if err == nil {
				t.Fatalf("expected %s to be rejected", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// The single most important behaviour in this plugin: changing one radio must
// not discard any other override. Whether the API merges or replaces is
// undocumented, so everything read is sent back.
func TestSetRadioChannel_ApplyPreservesOtherOverrides(t *testing.T) {
	f := newFakeController()
	p, _ := newTestPlugin(t, f)
	h := &setRadioChannel{p: p}
	ctx := context.Background()

	params := SetRadioChannelParams{
		MAC: "AA:BB:CC:DD:EE:FF", Band: "5GHz", RadioID: 2, Channel: "149",
	}
	plan, err := h.Plan(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Apply(ctx, params, plan); err != nil {
		t.Fatal(err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.putBodies) != 1 {
		t.Fatalf("PUT count = %d, want 1", len(f.putBodies))
	}
	body := f.putBodies[0]

	// ap_group is required alongside overrides; omitting it fails the schema
	// and guessing it relocates the device.
	if body["ap_group"] != "Campus-Indoor" {
		t.Fatalf("ap_group = %v, want Campus-Indoor", body["ap_group"])
	}

	overrides, ok := body["overrides"].(map[string]any)
	if !ok {
		t.Fatalf("overrides missing from the request body: %+v", body)
	}
	if overrides["location"] != "Building A" {
		t.Fatal("a non-radio override was dropped; changing a channel must not " +
			"discard unrelated configuration")
	}

	radios, _ := overrides["radios"].([]any)
	if len(radios) != 2 {
		t.Fatalf("radios = %d, want both preserved", len(radios))
	}

	byID := map[float64]map[string]any{}
	for _, r := range radios {
		m := r.(map[string]any)
		byID[m["id"].(float64)] = m
	}
	if byID[1]["channel"] != "6" {
		t.Fatal("the 2.4 GHz radio's channel was changed; only radio 2 was requested")
	}
	if byID[1]["power"] != "auto" {
		t.Fatal("the 2.4 GHz radio's power setting was dropped")
	}
	if byID[2]["channel"] != "149" {
		t.Fatalf("target radio channel = %v, want 149", byID[2]["channel"])
	}
	if byID[2]["channel_width"] != float64(80) {
		t.Fatal("the target radio's existing width was dropped; the mutation did " +
			"not ask to change it")
	}
}

// A 5xx on a write means the outcome is unknown, and must be reported as such
// so the executor does not retry it.
func TestSetRadioChannel_ServerErrorIsIndeterminate(t *testing.T) {
	f := newFakeController()
	f.putStatus = http.StatusBadGateway
	p, _ := newTestPlugin(t, f)
	h := &setRadioChannel{p: p}
	ctx := context.Background()

	params := SetRadioChannelParams{
		MAC: "AA:BB:CC:DD:EE:FF", Band: "5GHz", RadioID: 2, Channel: "149",
	}
	plan, err := h.Plan(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.Apply(ctx, params, plan)
	if !errors.Is(err, operations.ErrIndeterminate) {
		t.Fatalf("err = %v, want it to wrap ErrIndeterminate", err)
	}
}

// A 4xx means the request was understood and refused, so nothing happened and
// the outcome is not ambiguous.
func TestSetRadioChannel_ClientErrorIsDefinite(t *testing.T) {
	f := newFakeController()
	f.putStatus = http.StatusUnprocessableEntity
	p, _ := newTestPlugin(t, f)
	h := &setRadioChannel{p: p}
	ctx := context.Background()

	params := SetRadioChannelParams{
		MAC: "AA:BB:CC:DD:EE:FF", Band: "5GHz", RadioID: 2, Channel: "149",
	}
	plan, _ := h.Plan(ctx, params)
	_, err := h.Apply(ctx, params, plan)
	if err == nil {
		t.Fatal("expected an error")
	}
	if errors.Is(err, operations.ErrIndeterminate) {
		t.Fatal("a 4xx means the request was refused, so the outcome is known, " +
			"not indeterminate")
	}
}

func TestSetRadioChannel_ObserveReadsBackTheChange(t *testing.T) {
	f := newFakeController()
	p, _ := newTestPlugin(t, f)
	h := &setRadioChannel{p: p}
	ctx := context.Background()

	params := SetRadioChannelParams{
		MAC: "AA:BB:CC:DD:EE:FF", Band: "5GHz", RadioID: 2, Channel: "149",
	}
	plan, _ := h.Plan(ctx, params)
	if _, err := h.Apply(ctx, params, plan); err != nil {
		t.Fatal(err)
	}

	observed, err := h.Observe(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if observed.Channel != "149" {
		t.Fatalf("observed channel = %q, want 149", observed.Channel)
	}
}

func TestReboot_RequiresReasonAndOnlineDevice(t *testing.T) {
	f := newFakeController()
	p, _ := newTestPlugin(t, f)
	h := &rebootDevice{p: p}
	ctx := context.Background()

	if _, err := h.Plan(ctx, RebootParams{MAC: "AA:BB:CC:DD:EE:FF"}); err == nil ||
		!strings.Contains(err.Error(), "reason is required") {
		t.Fatalf("a reboot without a reason should be refused, got %v", err)
	}

	f.mu.Lock()
	f.device["status"] = "offline"
	f.mu.Unlock()

	_, err := h.Plan(ctx, RebootParams{MAC: "AA:BB:CC:DD:EE:FF", Reason: "wedged"})
	if err == nil || !strings.Contains(err.Error(), "only an online device") {
		t.Fatalf("rebooting an offline device should be refused, got %v", err)
	}
}

func TestReboot_PlanReportsClientImpact(t *testing.T) {
	f := newFakeController()
	p, _ := newTestPlugin(t, f)
	h := &rebootDevice{p: p}

	plan, err := h.Plan(context.Background(), RebootParams{
		MAC: "AA:BB:CC:DD:EE:FF", Reason: "firmware wedged",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.Impact, "12 connected client") {
		t.Fatalf("impact should name the number of affected clients, got %q", plan.Impact)
	}
	// A reboot has no single target state, so nothing is declared desired and
	// the verifier has nothing to compare against.
	if plan.Desired.MAC != "" {
		t.Fatal("a reboot should declare no desired state")
	}
}

func TestIsAmbiguous(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"500", &APIError{StatusCode: 500}, true},
		{"502", &APIError{StatusCode: 502}, true},
		{"504", &APIError{StatusCode: 504}, true},
		{"400", &APIError{StatusCode: 400}, false},
		{"404", &APIError{StatusCode: 404}, false},
		{"422", &APIError{StatusCode: 422}, false},
		{"429", &APIError{StatusCode: 429}, false},
		{"transport failure", errors.New("connection reset"), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isAmbiguous(tc.err); got != tc.want {
				t.Fatalf("isAmbiguous(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func intPtr(i int) *int { return &i }
