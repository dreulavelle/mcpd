package bandwidth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fixedNow is the clock every test runs on, so a token's expiry is a fact
// rather than a race with the wall.
var fixedNow = time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)

func at(t time.Time) func() time.Time { return func() time.Time { return t } }

// jwt builds a token whose payload carries the given claims. Only the payload
// is real: nothing here verifies a signature, and the comment on decodeClaims
// says why that is safe.
func jwt(t *testing.T, payload string) string {
	t.Helper()
	enc := base64.RawURLEncoding.EncodeToString([]byte(payload))
	return "header." + enc + ".signature"
}

// tokenServer answers the credential exchange and counts how often it is
// asked, which is the whole point of caching.
type tokenServer struct {
	mu        sync.Mutex
	exchanges int
	expiresIn int
	accounts  string
	status    int
}

func (s *tokenServer) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.exchanges++
		status := s.status
		s.mu.Unlock()

		if status != 0 && status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		if r.URL.Path != tokenPath {
			t.Errorf("token exchange went to %s, want %s", r.URL.Path, tokenPath)
		}
		if r.Method != http.MethodPost {
			t.Errorf("token exchange used %s, want POST", r.Method)
		}
		// The credential belongs in the header, not the body.
		id, secret, ok := r.BasicAuth()
		if !ok || id != "client" || secret != "shh" {
			t.Errorf("credential missing or wrong: %q %q ok=%v", id, secret, ok)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if got := r.Form.Get("grant_type"); got != "client_credentials" {
			t.Errorf("grant_type = %q", got)
		}

		accounts := s.accounts
		if accounts == "" {
			accounts = `["9000001","9000002"]`
		}
		token := jwt(t, fmt.Sprintf(`{"accounts":%s,"exp":%d}`,
			accounts, fixedNow.Add(time.Hour).Unix()))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": token,
			"token_type":   "Bearer",
			"expires_in":   s.expiresIn,
		})
	}
}

func newTokenServer(t *testing.T, expiresIn int) (*tokenServer, *httptest.Server) {
	t.Helper()
	ts := &tokenServer{expiresIn: expiresIn}
	srv := httptest.NewServer(ts.handler(t))
	t.Cleanup(srv.Close)
	return ts, srv
}

// A token is exchanged once and reused. A request per API call would double
// this integration's traffic against an account that is metered.
func TestTokenIsCachedAndReused(t *testing.T) {
	ts, srv := newTokenServer(t, 3600)
	src := newTokenSource(srv.Client(), srv.URL, "client", "shh", at(fixedNow))

	for i := range 5 {
		got, err := src.Token(context.Background())
		if err != nil {
			t.Fatalf("token %d: %v", i, err)
		}
		if got == "" {
			t.Fatal("empty token")
		}
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.exchanges != 1 {
		t.Fatalf("exchanged %d times, want 1 -- the token is not being cached", ts.exchanges)
	}
}

// Renewed before it lapses rather than after, because renewing a minute early
// costs one round trip and renewing a second late costs a 401 in the middle of
// somebody's question.
func TestTokenIsRenewedBeforeItExpires(t *testing.T) {
	ts, srv := newTokenServer(t, 3600)
	now := fixedNow
	src := newTokenSource(srv.Client(), srv.URL, "client", "shh", func() time.Time { return now })

	if _, err := src.Token(context.Background()); err != nil {
		t.Fatalf("first: %v", err)
	}
	// Inside the margin of expiry: still valid to the second, but close
	// enough that it must be replaced.
	now = fixedNow.Add(time.Hour - refreshMargin/2)
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatalf("second: %v", err)
	}

	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.exchanges != 2 {
		t.Fatalf("exchanged %d times, want 2 -- a token inside the refresh "+
			"margin was reused", ts.exchanges)
	}
}

// A token with no expires_in still has an exp in its claims. Falling back to it
// beats treating the token as immortal and finding out through a 401.
func TestTokenFallsBackToTheClaimExpiry(t *testing.T) {
	_, srv := newTokenServer(t, 0)
	src := newTokenSource(srv.Client(), srv.URL, "client", "shh", at(fixedNow))

	if _, err := src.Token(context.Background()); err != nil {
		t.Fatalf("token: %v", err)
	}
	src.mu.Lock()
	defer src.mu.Unlock()
	if want := fixedNow.Add(time.Hour); !src.expires.Equal(want) {
		t.Fatalf("expiry = %s, want %s from the exp claim", src.expires.UTC(), want)
	}
}

// The accounts claim is what turns a 403 into a sentence naming the problem.
func TestAccountsComeFromTheTokenClaims(t *testing.T) {
	_, srv := newTokenServer(t, 3600)
	src := newTokenSource(srv.Client(), srv.URL, "client", "shh", at(fixedNow))

	got, err := src.Accounts(context.Background())
	if err != nil {
		t.Fatalf("accounts: %v", err)
	}
	if len(got) != 2 || got[0] != "9000001" || got[1] != "9000002" {
		t.Fatalf("accounts = %v", got)
	}
}

// Bandwidth account ids are numeric and a JSON document may write one either
// way; both have to come back as the same string.
func TestAccountClaimAcceptsNumbersAndStrings(t *testing.T) {
	for name, claim := range map[string]string{
		"strings": `["9000001"]`,
		"numbers": `[9000001]`,
	} {
		t.Run(name, func(t *testing.T) {
			token := jwt(t, fmt.Sprintf(`{"accounts":%s}`, claim))
			got := decodeClaims(token)
			if len(got.Accounts) != 1 || got.Accounts[0] != "9000001" {
				t.Fatalf("accounts = %v", got.Accounts)
			}
		})
	}
}

// A token that is not a JWT is not a crash. Nothing is authorised on these
// claims; they are read to say something useful, and an unreadable one simply
// goes unsaid.
func TestUnreadableClaimsAreEmptyRatherThanFatal(t *testing.T) {
	for _, token := range []string{"", "not-a-jwt", "a.b", "a.!!!.c", "a." +
		base64.RawURLEncoding.EncodeToString([]byte("not json")) + ".c"} {
		if got := decodeClaims(token); len(got.Accounts) != 0 || got.Expiry != 0 {
			t.Errorf("decodeClaims(%q) = %+v, want empty", token, got)
		}
	}
}

// A refused credential must say which of the two fixable things is wrong, and
// must not be reported as though the API itself were down.
func TestTokenFailuresSayWhatToChange(t *testing.T) {
	for status, want := range map[int]string{
		http.StatusUnauthorized:    "expiry date set when it was created",
		http.StatusForbidden:       "at least one",
		http.StatusTooManyRequests: "exchanging the same credential in a loop",
	} {
		ts, srv := newTokenServer(t, 3600)
		ts.status = status
		src := newTokenSource(srv.Client(), srv.URL, "client", "shh", at(fixedNow))

		_, err := src.Token(context.Background())
		if err == nil {
			t.Fatalf("status %d produced no error", status)
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("status %d: message does not say %q: %v", status, want, err)
		}
	}
}

// The secret must not survive into an error that reaches a log or a dashboard.
func TestTheSecretIsRedactedFromErrors(t *testing.T) {
	err := fmt.Errorf("dial failed for client:%s@host", "shh")
	got := redactSecret(err, "shh")
	if strings.Contains(got.Error(), "shh") {
		t.Fatalf("the secret survived redaction: %v", got)
	}
	if !strings.Contains(got.Error(), "[REDACTED]") {
		t.Fatalf("redaction left no marker: %v", got)
	}
}
