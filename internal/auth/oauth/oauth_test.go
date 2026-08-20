package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/storage/sqlite"
)

var clock = time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

type fixture struct {
	srv    *Server
	store  *Store
	mux    *http.ServeMux
	user   *User
	client *Client
	now    *time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()

	db, err := sqlite.Open(ctx, sqlite.Options{
		Path:              filepath.Join(t.TempDir(), "oauth.db"),
		RelaxedDurability: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := sqlite.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	now := clock
	nowFn := func() time.Time { return now }
	store := NewStore(db, nowFn)

	user, err := store.CreateUserWithPassword(ctx, CreateUserRequest{
		Username: "alice", Password: "correct-horse-battery",
		Role: "approver", Plugins: []string{"cnmaestro", "echo"},
	})
	if err != nil {
		t.Fatal(err)
	}

	client := &Client{
		ID:           "cli_test",
		Name:         "Test Client",
		RedirectURIs: []string{"https://client.test.invalid/callback"},
		Type:         RegDynamic,
	}
	if err := store.UpsertClient(ctx, client, ""); err != nil {
		t.Fatal(err)
	}

	srv, err := NewServer(Config{
		Issuer:                   "https://mcp.test.invalid",
		AllowDynamicRegistration: true,
		AllowCIMD:                true,
	}, store, slog.New(slog.NewTextHandler(io.Discard, nil)), nowFn, nil)
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	srv.Routes(mux)
	return &fixture{srv: srv, store: store, mux: mux, user: user, client: client, now: &now}
}

// pkce returns a matched verifier and S256 challenge.
func pkce() (verifier, challenge string) {
	verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// authorize drives the consent form and returns the issued code.
func (f *fixture) authorize(t *testing.T, challenge, scope string) string {
	t.Helper()
	form := url.Values{
		"client_id":             {f.client.ID},
		"redirect_uri":          {f.client.RedirectURIs[0]},
		"response_type":         {"code"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"scope":                 {scope},
		"state":                 {"xyz"},
		"username":              {"alice"},
		"password":              {"correct-horse-battery"},
		"action":                {"allow"},
	}
	r := httptest.NewRequest(http.MethodPost, "/oauth/authorize", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)

	if w.Code != http.StatusFound {
		t.Fatalf("authorize status = %d, want 302: %s", w.Code, w.Body.String())
	}
	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if got := loc.Query().Get("state"); got != "xyz" {
		t.Fatalf("state = %q, want xyz (CSRF protection depends on it round-tripping)", got)
	}
	code := loc.Query().Get("code")
	if code == "" {
		t.Fatalf("no code in redirect: %s", loc.String())
	}
	return code
}

// exchange redeems a code at the token endpoint.
func (f *fixture) exchange(t *testing.T, code, verifier string) (*httptest.ResponseRecorder, *tokenResponse) {
	t.Helper()
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {f.client.RedirectURIs[0]},
		"client_id":     {f.client.ID},
		"code_verifier": {verifier},
	}
	r := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)

	var resp tokenResponse
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
	}
	return w, &resp
}

func TestFullAuthorizationCodeFlow(t *testing.T) {
	f := newFixture(t)
	verifier, challenge := pkce()

	code := f.authorize(t, challenge, ScopeRead+" "+ScopePropose+" "+PluginScope("cnmaestro"))
	w, tok := f.exchange(t, code, verifier)

	if w.Code != http.StatusOK {
		t.Fatalf("token exchange = %d: %s", w.Code, w.Body.String())
	}
	if tok.AccessToken == "" || tok.RefreshToken == "" {
		t.Fatal("both an access and a refresh token must be issued")
	}
	if tok.TokenType != "Bearer" {
		t.Fatalf("token_type = %q, want Bearer", tok.TokenType)
	}

	// The access token must resolve to a principal scoped exactly as granted.
	v := NewVerifier(f.store, func() time.Time { return *f.now })
	p, err := v.Verify(context.Background(), tok.AccessToken, nil)
	if err != nil {
		t.Fatalf("issued token did not verify: %v", err)
	}
	if !p.CanAccessPlugin("cnmaestro") {
		t.Fatal("principal should reach the granted plugin")
	}
	if p.CanAccessPlugin("echo") {
		t.Fatal("principal must not reach a plugin that was not in the token scope, " +
			"even though the user themselves is granted it")
	}
	if !p.Distinguishable {
		t.Fatal("an OAuth principal is a specific user and must be distinguishable")
	}
}

// A code is single-use. Replaying it must fail even with a valid verifier.
func TestAuthCode_IsSingleUse(t *testing.T) {
	f := newFixture(t)
	verifier, challenge := pkce()
	code := f.authorize(t, challenge, ScopeRead)

	if w, _ := f.exchange(t, code, verifier); w.Code != http.StatusOK {
		t.Fatalf("first exchange failed: %s", w.Body.String())
	}
	w, _ := f.exchange(t, code, verifier)
	if w.Code == http.StatusOK {
		t.Fatal("replaying an authorization code must fail")
	}
}

func TestTokenExchange_Rejects(t *testing.T) {
	verifier, challenge := pkce()

	tests := []struct {
		name   string
		mutate func(*fixture, url.Values)
	}{
		{"wrong code verifier", func(_ *fixture, v url.Values) {
			v.Set("code_verifier", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
		}},
		{"missing code verifier", func(_ *fixture, v url.Values) {
			v.Del("code_verifier")
		}},
		{"mismatched redirect uri", func(_ *fixture, v url.Values) {
			v.Set("redirect_uri", "https://attacker.test.invalid/callback")
		}},
		{"unknown client", func(_ *fixture, v url.Values) {
			v.Set("client_id", "cli_does_not_exist")
		}},
		{"unknown code", func(_ *fixture, v url.Values) {
			v.Set("code", "not-a-real-code")
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			code := f.authorize(t, challenge, ScopeRead)

			form := url.Values{
				"grant_type":    {"authorization_code"},
				"code":          {code},
				"redirect_uri":  {f.client.RedirectURIs[0]},
				"client_id":     {f.client.ID},
				"code_verifier": {verifier},
			}
			tc.mutate(f, form)

			r := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			f.mux.ServeHTTP(w, r)

			if w.Code == http.StatusOK {
				t.Fatalf("exchange should have failed: %s", w.Body.String())
			}
		})
	}
}

// Replaying a rotated refresh token means it was captured, since the
// legitimate client and an attacker cannot both hold the current one. The
// whole lineage must be revoked.
func TestRefreshToken_ReuseRevokesLineage(t *testing.T) {
	f := newFixture(t)
	verifier, challenge := pkce()
	code := f.authorize(t, challenge, ScopeRead+" "+PluginScope("cnmaestro"))
	_, first := f.exchange(t, code, verifier)

	refresh := func(token string) (*httptest.ResponseRecorder, *tokenResponse) {
		form := url.Values{
			"grant_type":    {"refresh_token"},
			"refresh_token": {token},
			"client_id":     {f.client.ID},
		}
		r := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, r)
		var resp tokenResponse
		if w.Code == http.StatusOK {
			_ = json.Unmarshal(w.Body.Bytes(), &resp)
		}
		return w, &resp
	}

	w, second := refresh(first.RefreshToken)
	if w.Code != http.StatusOK {
		t.Fatalf("first refresh failed: %s", w.Body.String())
	}
	if second.RefreshToken == first.RefreshToken {
		t.Fatal("refresh tokens must rotate")
	}

	// Replaying the original is the reuse signal.
	if w, _ := refresh(first.RefreshToken); w.Code == http.StatusOK {
		t.Fatal("a rotated refresh token must not be redeemable again")
	}

	// And the successor must have been revoked along with it.
	if w, _ := refresh(second.RefreshToken); w.Code == http.StatusOK {
		t.Fatal("detecting reuse must revoke the whole lineage, including the current token")
	}

	// The access token from the rotation must be dead too.
	v := NewVerifier(f.store, func() time.Time { return *f.now })
	if _, err := v.Verify(context.Background(), second.AccessToken, nil); err == nil {
		t.Fatal("access tokens in a revoked lineage must stop verifying")
	}
}

// A user cannot delegate more than they hold.
func TestGrantScope_CannotExceedUserRole(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	operator, err := f.store.CreateUserWithPassword(ctx, CreateUserRequest{
		Username: "bob", Password: "another-long-password",
		Role: "operator", Plugins: []string{"echo"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// An operator holds propose but not approve.
	granted := f.srv.grantScope(
		ScopeRead+" "+ScopePropose+" "+ScopeApprove+" "+PluginScope("echo"), operator)

	if !HasScope(granted, ScopePropose) {
		t.Error("operator should be able to delegate propose")
	}
	if HasScope(granted, ScopeApprove) {
		t.Error("an operator must not be able to delegate approve")
	}

	// Nor a plugin they do not hold.
	granted = f.srv.grantScope(ScopeRead+" "+PluginScope("cnmaestro"), operator)
	if HasScope(granted, PluginScope("cnmaestro")) {
		t.Error("a user must not delegate a plugin they are not granted")
	}
}

func TestVerifier_RejectsInactiveCredentials(t *testing.T) {
	f := newFixture(t)
	verifier, challenge := pkce()
	code := f.authorize(t, challenge, ScopeRead+" "+PluginScope("echo"))
	_, tok := f.exchange(t, code, verifier)

	v := NewVerifier(f.store, func() time.Time { return *f.now })
	ctx := context.Background()

	if _, err := v.Verify(ctx, tok.AccessToken, nil); err != nil {
		t.Fatalf("a fresh token should verify: %v", err)
	}

	// A refresh token must not authenticate an API call.
	if _, err := v.Verify(ctx, tok.RefreshToken, nil); err == nil {
		t.Fatal("a refresh token must not be accepted as an access token")
	}

	for _, bad := range []string{"", "garbage", tok.AccessToken + "x"} {
		if _, err := v.Verify(ctx, bad, nil); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Errorf("Verify(%q) = %v, want ErrUnauthenticated", bad, err)
		}
	}

	// Expiry.
	*f.now = clock.Add(2 * time.Hour)
	if _, err := v.Verify(ctx, tok.AccessToken, nil); err == nil {
		t.Fatal("an expired token must not verify")
	}
}

func TestAuthorize_RefusesUnregisteredRedirect(t *testing.T) {
	f := newFixture(t)
	_, challenge := pkce()

	q := url.Values{
		"client_id":             {f.client.ID},
		"redirect_uri":          {"https://attacker.test.invalid/steal"},
		"response_type":         {"code"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	r := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil)
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)

	// Critically, this must NOT be a redirect: bouncing an error to an
	// unvalidated URI is what makes an authorization server an open redirector.
	if w.Code == http.StatusFound {
		t.Fatalf("an unregistered redirect_uri must not be redirected to; got Location %q",
			w.Header().Get("Location"))
	}
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestAuthorize_RequiresPKCE(t *testing.T) {
	f := newFixture(t)

	tests := []struct{ name, challenge, method string }{
		{"missing challenge", "", "S256"},
		{"plain method refused", "abc", "plain"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q := url.Values{
				"client_id":     {f.client.ID},
				"redirect_uri":  {f.client.RedirectURIs[0]},
				"response_type": {"code"},
			}
			if tc.challenge != "" {
				q.Set("code_challenge", tc.challenge)
			}
			q.Set("code_challenge_method", tc.method)

			r := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil)
			w := httptest.NewRecorder()
			f.mux.ServeHTTP(w, r)

			// The redirect URI is registered, so the error is redirected --
			// but it must be an error, not a code.
			if w.Code == http.StatusFound {
				loc, _ := url.Parse(w.Header().Get("Location"))
				if loc.Query().Get("error") == "" {
					t.Fatal("expected an error, got a successful authorization")
				}
				return
			}
			if w.Code == http.StatusOK {
				t.Fatal("expected PKCE to be required")
			}
		})
	}
}

func TestVerifyPKCE(t *testing.T) {
	verifier, challenge := pkce()

	if err := VerifyPKCE(verifier, challenge, "S256"); err != nil {
		t.Fatalf("matching verifier rejected: %v", err)
	}
	if err := VerifyPKCE("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", challenge, "S256"); err == nil {
		t.Fatal("a mismatched verifier must be rejected")
	}
	if err := VerifyPKCE(verifier, challenge, "plain"); err == nil {
		t.Fatal("the plain method must be rejected")
	}
	if err := VerifyPKCE("short", challenge, "S256"); err == nil {
		t.Fatal("a verifier below the minimum length must be rejected")
	}
	if err := VerifyPKCE(strings.Repeat("a", 129), challenge, "S256"); err == nil {
		t.Fatal("a verifier above the maximum length must be rejected")
	}
	if err := VerifyPKCE(strings.Repeat("a", 50)+"!", challenge, "S256"); err == nil {
		t.Fatal("a verifier with characters outside the permitted set must be rejected")
	}
}

func TestDynamicRegistration(t *testing.T) {
	f := newFixture(t)

	body := `{"client_name":"ChatGPT","redirect_uris":["https://chatgpt.com/connector_platform_oauth_redirect"],"token_endpoint_auth_method":"none"}`
	r := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("registration = %d: %s", w.Code, w.Body.String())
	}
	var meta clientMetadata
	if err := json.Unmarshal(w.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}
	if meta.ClientID == "" {
		t.Fatal("a client_id must be issued")
	}
	if meta.ClientSecret != "" {
		t.Fatal("a client registering with token_endpoint_auth_method=none must not receive a secret")
	}

	stored, err := f.store.ClientByID(context.Background(), meta.ClientID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.IsPublic() {
		t.Fatal("the stored client should be public")
	}
}

func TestValidateRedirectURIs(t *testing.T) {
	tests := []struct {
		name  string
		uris  []string
		valid bool
	}{
		{"https", []string{"https://example.com/cb"}, true},
		{"loopback http", []string{"http://127.0.0.1:1234/cb"}, true},
		{"localhost http", []string{"http://localhost:1234/cb"}, true},
		{"private-use scheme", []string{"com.example.app:/cb"}, true},
		{"empty", nil, false},
		{"plaintext non-loopback", []string{"http://example.com/cb"}, false},
		{"fragment", []string{"https://example.com/cb#frag"}, false},
		{"bare custom scheme", []string{"myapp:/cb"}, false},
		{"no host", []string{"https:///cb"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRedirectURIs(tc.uris)
			if tc.valid && err != nil {
				t.Fatalf("%v should be valid: %v", tc.uris, err)
			}
			if !tc.valid && err == nil {
				t.Fatalf("%v should be rejected", tc.uris)
			}
		})
	}
}

// The client name is attacker-controlled text rendered on a consent screen, so
// it must not be able to impersonate the surrounding page.
func TestSanitizeDisplay(t *testing.T) {
	tests := []struct{ in, want string }{
		{"ChatGPT", "ChatGPT"},
		{"", "Unnamed client"},
		{"line\nbreak", "linebreak"},
		{"null\x00byte", "nullbyte"},
		{"‮gnihsihp", "gnihsihp"}, // right-to-left override stripped
		{strings.Repeat("x", 200), strings.Repeat("x", 64)},
	}
	for _, tc := range tests {
		if got := sanitizeDisplay(tc.in); got != tc.want {
			t.Errorf("sanitizeDisplay(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMetadataEndpoint(t *testing.T) {
	f := newFixture(t)
	r := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server", nil)
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("metadata = %d", w.Code)
	}
	var meta authServerMetadata
	if err := json.Unmarshal(w.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Issuer != "https://mcp.test.invalid" {
		t.Fatalf("issuer = %q", meta.Issuer)
	}
	if len(meta.CodeChallengeMethodsSupported) != 1 || meta.CodeChallengeMethodsSupported[0] != "S256" {
		t.Fatalf("only S256 should be advertised, got %v", meta.CodeChallengeMethodsSupported)
	}
	for _, want := range []string{"authorization_code", "refresh_token"} {
		if !contains(meta.GrantTypesSupported, want) {
			t.Errorf("metadata omits grant type %s", want)
		}
	}
}

func TestPasswordPolicy(t *testing.T) {
	if _, err := HashPassword("short"); err == nil {
		t.Error("a short password must be rejected")
	}
	// bcrypt truncates past 72 bytes, which would make two long passwords
	// equivalent; rejecting is safer than silently truncating.
	if _, err := HashPassword(strings.Repeat("a", 73)); err == nil {
		t.Error("an over-long password must be rejected rather than truncated")
	}
	h, err := HashPassword("a-perfectly-fine-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h, "a-perfectly-fine-passphrase") {
		t.Fatal("the hash must not contain the plaintext")
	}
}

func TestHashSecret_IsStableAndDistinct(t *testing.T) {
	a := HashSecret("token-a")
	if a != HashSecret("token-a") {
		t.Fatal("hashing must be deterministic for lookup to work")
	}
	if a == HashSecret("token-b") {
		t.Fatal("distinct secrets must hash differently")
	}
	if strings.Contains(a, "token-a") {
		t.Fatal("the digest must not contain the plaintext")
	}
}
