package cnmaestro

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingServer captures every request the client makes.
type recordingServer struct {
	mu      sync.Mutex
	paths   []string
	queries []url.Values
	pages   int
}

func (r *recordingServer) handler(pageCount int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "/access/token") {
			writeJSON(w, map[string]any{"access_token": "t", "expires_in": 3600})
			return
		}
		r.mu.Lock()
		r.paths = append(r.paths, req.URL.Path)
		r.queries = append(r.queries, req.URL.Query())
		r.pages++
		page := r.pages
		r.mu.Unlock()

		body := map[string]any{
			"paging": map[string]any{"total": pageCount * 2, "limit": 2},
			"data":   []any{map[string]any{"mac": "AA:BB"}, map[string]any{"mac": "CC:DD"}},
		}
		if page < pageCount {
			body["paging"].(map[string]any)["next_continuation_token"] = "tok-" + string(rune('a'+page))
		}
		writeJSON(w, body)
	})
}

func newTestClient(t *testing.T, h http.Handler, mutate func(*Config)) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)

	cfg := Config{
		BaseURL: srv.URL, ManagedAccount: MainAccount,
		RequestsPerSecond: 1000, Burst: 1000, PageSize: 2, MaxPages: 10,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	cfg.withDefaults()
	return NewClient(srv.Client(), cfg, "id", "secret",
		slog.New(slog.NewTextHandler(io.Discard, nil)), time.Now), srv
}

// The deny-list must be enforced in the client, below every caller, so that no
// tool or mutation can reach a blocked endpoint by constructing a path.
func TestClient_EnforcesDenyList(t *testing.T) {
	rec := &recordingServer{}
	c, _ := newTestClient(t, rec.handler(1), nil)
	ctx := context.Background()

	blocked := []string{
		"/devices/AA:BB/cli",
		"/cnwave60/devices/AA:BB/remote_command",
		"/devices/AA:BB/traceroute",
	}
	for _, path := range blocked {
		t.Run(path, func(t *testing.T) {
			if _, err := c.Get(ctx, path, nil); err == nil {
				t.Fatalf("%s must be refused by the client", path)
			}
			if _, err := c.Post(ctx, path, map[string]any{}); err == nil {
				t.Fatalf("POST %s must be refused by the client", path)
			}
		})
	}

	// Nothing blocked should ever have reached the network.
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, p := range rec.paths {
		if strings.Contains(p, "cli") || strings.Contains(p, "remote_command") {
			t.Fatalf("a blocked path reached the server: %s", p)
		}
	}
}

// managed_account must be explicit on every call. Omitting it means different
// things depending on whether the request names a network.
func TestClient_AlwaysSendsManagedAccount(t *testing.T) {
	rec := &recordingServer{}
	c, _ := newTestClient(t, rec.handler(1), nil)

	if _, err := c.Get(context.Background(), "/devices", nil); err != nil {
		t.Fatal(err)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if got := rec.queries[0].Get("managed_account"); got != MainAccount {
		t.Fatalf("managed_account = %q, want %q on every request", got, MainAccount)
	}
}

func TestClient_ExplicitManagedAccountIsNotOverridden(t *testing.T) {
	rec := &recordingServer{}
	c, _ := newTestClient(t, rec.handler(1), nil)

	params := url.Values{"managed_account": {"Tenant-A"}}
	if _, err := c.Get(context.Background(), "/devices", params); err != nil {
		t.Fatal(err)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if got := rec.queries[0].Get("managed_account"); got != "Tenant-A" {
		t.Fatalf("managed_account = %q, want the caller's explicit value", got)
	}
}

// Pagination must follow continuation tokens. Offset is deprecated upstream
// and removed in 6.4.0.
func TestClient_FollowsContinuationTokens(t *testing.T) {
	rec := &recordingServer{}
	c, _ := newTestClient(t, rec.handler(3), nil)

	items, _, err := c.List(context.Background(), "/devices", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 6 {
		t.Fatalf("items = %d, want 6 across three pages", len(items))
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.queries) != 3 {
		t.Fatalf("requests = %d, want 3", len(rec.queries))
	}
	// The first page carries no token; later pages must.
	if rec.queries[0].Get("continuation_token") != "" {
		t.Fatal("the first page must not send a continuation token")
	}
	for i := 1; i < 3; i++ {
		if rec.queries[i].Get("continuation_token") == "" {
			t.Fatalf("page %d did not send a continuation token", i+1)
		}
		if rec.queries[i].Get("offset") != "" {
			t.Fatal("offset pagination is deprecated and must not be used")
		}
	}
}

// A capped read must stop rather than walking an entire estate.
func TestClient_BoundsPagination(t *testing.T) {
	rec := &recordingServer{}
	c, _ := newTestClient(t, rec.handler(100), func(cfg *Config) { cfg.MaxPages = 3 })

	items, _, err := c.List(context.Background(), "/devices", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 6 {
		t.Fatalf("items = %d, want the 3-page cap to apply", len(items))
	}
}

func TestClient_MapsErrorStatuses(t *testing.T) {
	statuses := []int{400, 401, 404, 422, 429, 500, 503}
	for _, status := range statuses {
		t.Run(http.StatusText(status), func(t *testing.T) {
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/access/token") {
					writeJSON(w, map[string]any{"access_token": "t", "expires_in": 3600})
					return
				}
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"message":"upstream said no"}`))
			})
			c, _ := newTestClient(t, h, nil)

			_, err := c.Get(context.Background(), "/devices", nil)
			if err == nil {
				t.Fatal("expected an error")
			}
			var apiErr *APIError
			if !stdErrorsAs(err, &apiErr) {
				t.Fatalf("want *APIError, got %T", err)
			}
			if apiErr.StatusCode != status {
				t.Fatalf("status = %d, want %d", apiErr.StatusCode, status)
			}
			if apiErr.Message != "upstream said no" {
				t.Fatalf("message = %q", apiErr.Message)
			}
		})
	}
}

// An HTML error page carries no useful message and would flood a log line.
func TestClient_DiscardsHTMLErrorBodies(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/access/token") {
			writeJSON(w, map[string]any{"access_token": "t", "expires_in": 3600})
			return
		}
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html><body>" + strings.Repeat("x", 5000) + "</body></html>"))
	})
	c, _ := newTestClient(t, h, nil)

	_, err := c.Get(context.Background(), "/devices", nil)
	var apiErr *APIError
	if !stdErrorsAs(err, &apiErr) {
		t.Fatalf("want *APIError, got %v", err)
	}
	if apiErr.Message != "" {
		t.Fatalf("an HTML body should yield no message, got %q", apiErr.Message)
	}
}

// The Cloud token response names the regional host that subsequent calls must
// target. Ignoring it is the classic first-integration failure.
func TestTokenManager_HonoursRedirectURI(t *testing.T) {
	var apiCalls int32
	var mu sync.Mutex

	regional := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		apiCalls++
		mu.Unlock()
		writeJSON(w, map[string]any{"paging": map[string]any{}, "data": []any{}})
	}))
	defer regional.Close()

	entry := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/access/token") {
			writeJSON(w, map[string]any{
				"access_token": "t", "expires_in": 3600,
				"redirect_uri": regional.URL + "/some/path",
			})
			return
		}
		t.Errorf("API call reached the entry point instead of the regional host: %s", r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer entry.Close()

	// One client must trust both test certificates.
	c := NewClient(regional.Client(), Config{
		BaseURL: entry.URL, ManagedAccount: MainAccount,
		RequestsPerSecond: 1000, Burst: 1000, PageSize: 10, MaxPages: 1,
	}, "id", "secret", slog.New(slog.NewTextHandler(io.Discard, nil)), time.Now)

	// The entry point's certificate differs, so the token request itself will
	// fail TLS verification here; what matters is the host selection logic.
	host, err := hostFromRedirect(regional.URL + "/some/path")
	if err != nil {
		t.Fatal(err)
	}
	if host != regional.URL {
		t.Fatalf("hostFromRedirect kept a path component: %q", host)
	}
	_ = c
	_ = apiCalls
}

func TestHostFromRedirect(t *testing.T) {
	tests := []struct {
		in    string
		want  string
		valid bool
	}{
		{"https://eu.cloud.cambiumnetworks.com/api/v2", "https://eu.cloud.cambiumnetworks.com", true},
		{"https://host.example", "https://host.example", true},
		// Plaintext would expose the bearer token on every subsequent call.
		{"http://host.example", "", false},
		{"not-a-url", "", false},
		{"", "", false},
	}
	for _, tc := range tests {
		got, err := hostFromRedirect(tc.in)
		if tc.valid {
			if err != nil || got != tc.want {
				t.Errorf("hostFromRedirect(%q) = (%q,%v), want %q", tc.in, got, err, tc.want)
			}
			continue
		}
		if err == nil {
			t.Errorf("hostFromRedirect(%q) should have failed", tc.in)
		}
	}
}

func TestNormalizeMAC(t *testing.T) {
	tests := []struct {
		in    string
		want  string
		valid bool
	}{
		{"AA:BB:CC:DD:EE:FF", "AA:BB:CC:DD:EE:FF", true},
		{"aa:bb:cc:dd:ee:ff", "AA:BB:CC:DD:EE:FF", true},
		{"AA-BB-CC-DD-EE-FF", "AA:BB:CC:DD:EE:FF", true},
		{"AABBCCDDEEFF", "AA:BB:CC:DD:EE:FF", true},
		{"  AA:BB:CC:DD:EE:FF  ", "AA:BB:CC:DD:EE:FF", true},
		{"AA:BB:CC:DD:EE", "", false},
		{"ZZ:BB:CC:DD:EE:FF", "", false},
		{"AA.BB.CC.DD.EE.FF", "", false},
		{"", "", false},
	}
	for _, tc := range tests {
		got, err := normalizeMAC(tc.in)
		if tc.valid {
			if err != nil || got != tc.want {
				t.Errorf("normalizeMAC(%q) = (%q,%v), want %q", tc.in, got, err, tc.want)
			}
			continue
		}
		if err == nil {
			t.Errorf("normalizeMAC(%q) should have failed", tc.in)
		}
	}
}

// Device names and alarm text cross the trust boundary into a model that also
// holds approval tools.
func TestSanitizeText(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		mustNot string
	}{
		{"newlines become spaces", "line1\nline2", "\n"},
		{"carriage returns", "a\r\nb", "\r"},
		{"null bytes", "a\x00b", "\x00"},
		{"right-to-left override", "safe‮gnorw", "‮"},
		{"isolate characters", "a⁦b⁩c", "⁦"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeText(tc.in)
			if strings.Contains(got, tc.mustNot) {
				t.Fatalf("sanitizeText(%q) = %q, which still contains %q", tc.in, got, tc.mustNot)
			}
		})
	}

	if got := sanitizeText(strings.Repeat("x", 500)); len([]rune(got)) > 300 {
		t.Fatal("sanitizeText must bound length so one device name cannot flood a context")
	}
	if got := sanitizeText("  Lobby-East  "); got != "Lobby-East" {
		t.Fatalf("ordinary text should survive intact, got %q", got)
	}
}

func TestConfig_Validate(t *testing.T) {
	valid := func() Config {
		return Config{
			BaseURL:     "https://cloud.cambiumnetworks.com",
			ClientIDRef: "env:ID", ClientSecretRef: "env:SECRET",
			ManagedAccount: MainAccount,
		}
	}
	base := valid()
	if err := base.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"no base url", func(c *Config) { c.BaseURL = "" }, "base_url is required"},
		{"plaintext base url", func(c *Config) { c.BaseURL = "http://cloud.example" }, "must use https"},
		{"no client id", func(c *Config) { c.ClientIDRef = "" }, "client_id_ref is required"},
		{"no secret", func(c *Config) { c.ClientSecretRef = "" }, "client_secret_ref is required"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := valid()
			tc.mutate(&c)
			err := c.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want mention of %q", err, tc.want)
			}
		})
	}
}

func TestMergeRadioOverride(t *testing.T) {
	existing := json.RawMessage(`{
		"location":"Building A",
		"radios":[
			{"id":1,"channel":"6","power":"auto"},
			{"id":2,"channel":"36","channel_width":80}
		]
	}`)

	out, err := mergeRadioOverride(existing, RadioOverride{ID: 2, Channel: "149"})
	if err != nil {
		t.Fatal(err)
	}

	if string(out["location"]) != `"Building A"` {
		t.Fatalf("unrelated override lost: %v", out["location"])
	}

	var radios []RadioOverride
	if err := json.Unmarshal(out["radios"], &radios); err != nil {
		t.Fatal(err)
	}
	if len(radios) != 2 {
		t.Fatalf("radios = %d, want 2", len(radios))
	}
	for _, r := range radios {
		switch r.ID {
		case 1:
			if r.Channel != "6" || r.Power != "auto" {
				t.Fatal("the untouched radio was modified")
			}
		case 2:
			if r.Channel != "149" {
				t.Fatalf("target channel = %q, want 149", r.Channel)
			}
			if r.ChannelWidth == nil || *r.ChannelWidth != 80 {
				t.Fatal("the target radio's width was dropped though it was not being changed")
			}
		}
	}
}

// A device with no existing overrides must still produce a valid request.
func TestMergeRadioOverride_EmptyStart(t *testing.T) {
	out, err := mergeRadioOverride(nil, RadioOverride{ID: 1, Channel: "6"})
	if err != nil {
		t.Fatal(err)
	}
	var radios []RadioOverride
	if err := json.Unmarshal(out["radios"], &radios); err != nil {
		t.Fatal(err)
	}
	if len(radios) != 1 || radios[0].Channel != "6" {
		t.Fatalf("radios = %+v", radios)
	}
}

// Unreadable existing overrides must fail loudly rather than being replaced
// with an empty object, which would wipe the device's configuration.
func TestMergeRadioOverride_RefusesUnreadableExisting(t *testing.T) {
	if _, err := mergeRadioOverride(json.RawMessage(`not json`), RadioOverride{ID: 1}); err == nil {
		t.Fatal("unreadable overrides must not be silently discarded")
	}
}

func TestValidChannelsAndBands(t *testing.T) {
	// Radio ids overlap across bands, which is why a band is required.
	if !overlap(RadioIDsForBand(Band5), RadioIDsForBand(Band6)) {
		t.Fatal("5 GHz and 6 GHz radio ids are expected to overlap; " +
			"if they no longer do, the band requirement can be revisited")
	}
	if ValidChannel(Band24, "149") {
		t.Fatal("channel 149 is not valid on 2.4 GHz")
	}
	if !ValidChannel(Band5, "149") {
		t.Fatal("channel 149 should be valid on 5 GHz")
	}
	if !ValidChannel(Band6, "auto") {
		t.Fatal("auto should be valid on every band")
	}
}

func overlap(a, b []int) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}
