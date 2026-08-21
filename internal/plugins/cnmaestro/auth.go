package cnmaestro

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// tokenResponse is the client-credentials response.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`

	// RedirectURI names the host that data calls must actually target.
	//
	// Cloud accounts are regionally sharded. Tokens are issued by the front
	// door and used against a region, so a client that authenticates against
	// cloud.cambiumnetworks.com and then calls it for data holds a valid token
	// pointed at the wrong shard. Nothing about the token response looks
	// wrong; the reads just return nothing useful.
	RedirectURI string `json:"redirect_uri"`
}

// tokenManager obtains, holds and refreshes the access token, and remembers
// which host the token said to use.
type tokenManager struct {
	http     *http.Client
	tokenURL string
	clientID string
	secret   string
	now      func() time.Time

	// refreshing serialises refreshes so a burst of tool calls produces one
	// token request rather than one per call.
	refreshing sync.Mutex

	mu          sync.RWMutex
	accessToken string
	expiry      time.Time
	apiHost     string
}

func newTokenManager(client *http.Client, baseURL, clientID, secret string, now func() time.Time) *tokenManager {
	if now == nil {
		now = time.Now
	}
	base := strings.TrimRight(baseURL, "/")
	return &tokenManager{
		http:     client,
		tokenURL: base + tokenPath,
		clientID: clientID,
		secret:   secret,
		now:      now,
		// Until a token says otherwise, the front door is the best guess.
		apiHost: base,
	}
}

// refreshSkew renews a token before it expires.
//
// Refreshing ahead of time rather than on a 401 keeps credential handling out
// of the error path, where it would be tangled with retries, rate limiting and
// every other reason a request can fail.
const refreshSkew = 5 * time.Minute

// token returns a valid access token and the host to send it to.
func (t *tokenManager) token(ctx context.Context) (token, host string, err error) {
	t.mu.RLock()
	tok, exp, h := t.accessToken, t.expiry, t.apiHost
	t.mu.RUnlock()
	if tok != "" && t.now().Before(exp.Add(-refreshSkew)) {
		return tok, h, nil
	}

	t.refreshing.Lock()
	defer t.refreshing.Unlock()

	// Another caller may have refreshed while this one waited.
	t.mu.RLock()
	tok, exp, h = t.accessToken, t.expiry, t.apiHost
	t.mu.RUnlock()
	if tok != "" && t.now().Before(exp.Add(-refreshSkew)) {
		return tok, h, nil
	}
	return t.refresh(ctx)
}

// refresh obtains a new token. The caller holds refreshing.
func (t *tokenManager) refresh(ctx context.Context) (string, string, error) {
	form := url.Values{"grant_type": {"client_credentials"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.tokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", fmt.Errorf("cnmaestro: build token request: %w", err)
	}
	// Credentials in the header rather than the body. The API accepts either,
	// and a body is the thing most likely to be logged by a proxy or captured
	// in a diagnostic.
	basic := base64.StdEncoding.EncodeToString([]byte(t.clientID + ":" + t.secret))
	req.Header.Set("Authorization", "Basic "+basic)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := t.http.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("cnmaestro: reach %s: %w", redactURL(t.tokenURL), err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", fmt.Errorf("cnmaestro: read token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", explainTokenFailure(resp.StatusCode, body)
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", "", fmt.Errorf("cnmaestro: token response was not JSON: %w", err)
	}
	if tr.AccessToken == "" {
		return "", "", fmt.Errorf("cnmaestro: token response carried no access_token")
	}

	host := t.hostFrom(tr.RedirectURI)
	expiry := t.now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	if tr.ExpiresIn <= 0 {
		// Documented as an hour. A response without one is treated as short
		// rather than eternal, so a missing field costs a refresh rather than
		// leaving a dead token in place.
		expiry = t.now().Add(time.Hour)
	}

	t.mu.Lock()
	t.accessToken, t.expiry, t.apiHost = tr.AccessToken, expiry, host
	t.mu.Unlock()

	return tr.AccessToken, host, nil
}

// hostFrom resolves where data calls should go.
//
// A redirect_uri that does not parse is ignored rather than fatal: the token
// itself is usable, and the front door is a workable fallback for a
// single-region account. Refusing here would turn a cosmetic upstream change
// into an outage.
func (t *tokenManager) hostFrom(redirect string) string {
	raw := strings.TrimSpace(redirect)
	if raw == "" {
		t.mu.RLock()
		defer t.mu.RUnlock()
		return t.apiHost
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		t.mu.RLock()
		defer t.mu.RUnlock()
		return t.apiHost
	}
	return strings.TrimRight(u.Scheme+"://"+u.Host, "/")
}

// invalidate drops the cached token, so the next call obtains a fresh one.
//
// Used when the API rejects a credential that had not yet expired, which
// happens when it is revoked or rotated upstream.
func (t *tokenManager) invalidate() {
	t.mu.Lock()
	t.accessToken, t.expiry = "", time.Time{}
	t.mu.Unlock()
}

// host reports the address data calls go to, for diagnostics.
func (t *tokenManager) host() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.apiHost
}

// explainTokenFailure turns a token rejection into something actionable.
//
// The distinction that matters is whether the credentials are wrong or the
// account cannot use the API at all, because those have different fixes and
// the raw response says neither clearly.
func explainTokenFailure(status int, body []byte) error {
	switch status {
	case http.StatusUnauthorized, http.StatusBadRequest:
		return fmt.Errorf("cnmaestro: those API credentials were rejected. " +
			"Check client_id and client_secret against Download Credentials " +
			"in cnMaestro under Application > API Clients")
	case http.StatusForbidden:
		return fmt.Errorf("cnmaestro: those credentials are valid but not " +
			"permitted to use the API. The API requires an appropriate " +
			"cnMaestro subscription")
	case http.StatusTooManyRequests:
		return fmt.Errorf("cnmaestro: rate limited while obtaining a token")
	}
	return fmt.Errorf("cnmaestro: token request failed: %s", summarise(status, body))
}
