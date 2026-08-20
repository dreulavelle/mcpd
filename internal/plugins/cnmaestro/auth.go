package cnmaestro

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// tokenResponse is the OAuth client-credentials response.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	// RedirectURI is cnMaestro-specific and easy to miss. Cloud accounts are
	// regionally sharded, and the Cloud token response names the host that
	// subsequent calls must actually target. Ignoring it is the classic
	// first-integration failure.
	RedirectURI string `json:"redirect_uri"`
}

// tokenManager obtains and refreshes the API token.
type tokenManager struct {
	httpClient *http.Client
	baseURL    string
	clientID   string
	secret     string
	now        func() time.Time

	mu sync.RWMutex
	// token and expiry are the current credential.
	token  string
	expiry time.Time
	// apiHost is where API calls go, which may differ from baseURL once the
	// token response has named a region.
	apiHost string
	// refreshing serialises concurrent refreshes so a burst of tool calls
	// produces one token request rather than many.
	refreshing sync.Mutex
}

func newTokenManager(client *http.Client, baseURL, clientID, secret string, now func() time.Time) *tokenManager {
	if now == nil {
		now = time.Now
	}
	return &tokenManager{
		httpClient: client,
		baseURL:    strings.TrimRight(baseURL, "/"),
		clientID:   clientID,
		secret:     secret,
		now:        now,
		apiHost:    strings.TrimRight(baseURL, "/"),
	}
}

// refreshSkew renews a token before it expires.
//
// Refreshing proactively rather than on a 401 keeps credential handling out of
// the error path, where it would be entangled with retries, rate limiting, and
// every other reason a request can fail.
const refreshSkew = 5 * time.Minute

// Token returns a valid access token, refreshing if needed.
func (t *tokenManager) Token(ctx context.Context) (string, error) {
	t.mu.RLock()
	tok, exp := t.token, t.expiry
	t.mu.RUnlock()

	if tok != "" && t.now().Add(refreshSkew).Before(exp) {
		return tok, nil
	}

	t.refreshing.Lock()
	defer t.refreshing.Unlock()

	// Re-check: another goroutine may have refreshed while we waited.
	t.mu.RLock()
	tok, exp = t.token, t.expiry
	t.mu.RUnlock()
	if tok != "" && t.now().Add(refreshSkew).Before(exp) {
		return tok, nil
	}

	return t.fetch(ctx)
}

// APIHost returns the host API calls should target.
func (t *tokenManager) APIHost() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.apiHost
}

// fetch performs the client-credentials exchange.
func (t *tokenManager) fetch(ctx context.Context) (string, error) {
	form := url.Values{"grant_type": {"client_credentials"}}
	endpoint := t.baseURL + "/api/v2/access/token"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("cnmaestro: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(t.clientID, t.secret)

	resp, err := t.httpClient.Do(req)
	if err != nil {
		// The error may quote the request URL, which carries no secret here,
		// but the credential is in a header that some transports echo.
		return "", fmt.Errorf("cnmaestro: token request failed: %w", scrubURLError(err))
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", fmt.Errorf("cnmaestro: read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		// The body is not included: a failed auth response can echo the
		// credential that was presented.
		return "", fmt.Errorf(
			"cnmaestro: token request returned %d; check the configured client id and secret",
			resp.StatusCode)
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", fmt.Errorf("cnmaestro: token response is not valid JSON: %w", err)
	}
	if tr.AccessToken == "" {
		return "", fmt.Errorf("cnmaestro: token response contained no access_token")
	}

	lifetime := time.Duration(tr.ExpiresIn) * time.Second
	if lifetime <= 0 {
		// Documented as 3600. Falling back keeps a malformed response from
		// producing a token that is treated as immediately expired, which
		// would refresh in a loop.
		lifetime = time.Hour
	}

	apiHost := t.baseURL
	if tr.RedirectURI != "" {
		if h, err := hostFromRedirect(tr.RedirectURI); err == nil {
			apiHost = h
		}
	}

	t.mu.Lock()
	t.token = tr.AccessToken
	t.expiry = t.now().Add(lifetime)
	t.apiHost = apiHost
	t.mu.Unlock()

	return tr.AccessToken, nil
}

// hostFromRedirect extracts the API base from the token response's
// redirect_uri, keeping only scheme and host so that a path component cannot
// redirect calls somewhere unexpected.
func hostFromRedirect(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme != "https" || u.Host == "" {
		return "", fmt.Errorf("cnmaestro: redirect_uri %q is not an https URL", raw)
	}
	return u.Scheme + "://" + u.Host, nil
}

// scrubURLError removes credentials from a *url.Error, which embeds the
// request URL and would otherwise carry any userinfo into a log line.
func scrubURLError(err error) error {
	var ue *url.Error
	if !errorsAs(err, &ue) {
		return err
	}
	if u, perr := url.Parse(ue.URL); perr == nil && u.User != nil {
		u.User = url.UserPassword("redacted", "redacted")
		ue.URL = u.String()
	}
	return ue
}
