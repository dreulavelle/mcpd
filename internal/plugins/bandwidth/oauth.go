package bandwidth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// tokenPath is where a client credential is exchanged for a bearer token.
const tokenPath = "/api/v1/oauth2/token"

// refreshMargin is how long before expiry a cached token is replaced.
//
// Generous, because the cost of being wrong is asymmetric: renewing a minute
// early costs one extra round trip, and renewing a second late costs a 401 in
// the middle of somebody's question. It also absorbs the clock skew between
// this host and Bandwidth's, which nothing here can measure.
const refreshMargin = 60 * time.Second

// tokenSource mints and caches the bearer token.
//
// Bandwidth issues API credentials as an OAuth2 client id and secret, and
// every call needs a short-lived token exchanged for them. The exchange is
// cached because it is the same answer for the life of the token and a token
// request per API call would double this integration's traffic.
type tokenSource struct {
	client   *http.Client
	tokenURL string
	id       string
	secret   string
	now      func() time.Time

	// mu guards the cached token and serialises the exchange, so a burst of
	// calls arriving on an expired token produces one request rather than one
	// per caller.
	mu      sync.Mutex
	token   string
	expires time.Time
	// accounts is what the credential is scoped to, as the token itself
	// reports it. Cached alongside the token because it arrives with it.
	accounts []string
}

func newTokenSource(client *http.Client, base, id, secret string, now func() time.Time) *tokenSource {
	return &tokenSource{
		client:   client,
		tokenURL: strings.TrimRight(base, "/") + tokenPath,
		id:       id,
		secret:   secret,
		now:      now,
	}
}

// Token returns a valid bearer token, exchanging for a new one if the cached
// one is missing or close to expiry.
func (t *tokenSource) Token(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.token != "" && t.now().Before(t.expires.Add(-refreshMargin)) {
		return t.token, nil
	}
	if err := t.exchange(ctx); err != nil {
		return "", err
	}
	return t.token, nil
}

// Accounts reports the account ids this credential may reach, from the last
// exchange. Empty until one has happened.
func (t *tokenSource) Accounts(ctx context.Context) ([]string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.token == "" || !t.now().Before(t.expires.Add(-refreshMargin)) {
		if err := t.exchange(ctx); err != nil {
			return nil, err
		}
	}
	out := make([]string, len(t.accounts))
	copy(out, t.accounts)
	return out, nil
}

// exchange performs the client-credentials grant. The caller holds mu.
func (t *tokenSource) exchange(ctx context.Context) error {
	body := strings.NewReader(url.Values{"grant_type": {"client_credentials"}}.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.tokenURL, body)
	if err != nil {
		return fmt.Errorf("bandwidth: build the token request: %w", err)
	}
	// The credential goes in the header rather than the form. Both are
	// permitted by RFC 6749; the header is the one that does not end up in an
	// access log that happens to record request bodies.
	req.SetBasicAuth(t.id, t.secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("bandwidth: reaching %s for a token: %w",
			redactURL(t.tokenURL), redactSecret(err, t.secret))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return explainTokenFailure(resp.StatusCode, redactURL(t.tokenURL))
	}

	var out struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("bandwidth: %s answered the token request with "+
			"something that is not JSON; the address may be reaching a proxy "+
			"rather than the API", redactURL(t.tokenURL))
	}
	if out.AccessToken == "" {
		return fmt.Errorf("bandwidth: %s answered the token request without a "+
			"token", redactURL(t.tokenURL))
	}

	t.token = out.AccessToken
	// expires_in is a lifetime in seconds and here it is genuinely that: it
	// describes the token this response just minted, so measuring from now is
	// correct. That is worth saying only because the same field name means
	// something different in the ExtremeCloud IQ integration, where it
	// describes a credential the caller already held.
	t.expires = t.now().Add(time.Duration(out.ExpiresIn) * time.Second)

	claims := decodeClaims(out.AccessToken)
	t.accounts = claims.Accounts
	// A token whose lifetime the response did not give still has one in its
	// claims. Falling back to it beats treating the token as immortal and
	// discovering otherwise through a 401.
	if out.ExpiresIn == 0 && claims.Expiry > 0 {
		t.expires = time.Unix(claims.Expiry, 0)
	}
	return nil
}

// claims are the parts of Bandwidth's token this integration reads.
type claims struct {
	// Accounts is which Bandwidth accounts the credential was scoped to when
	// it was created. It is the answer to "why can this credential not see
	// that", which is otherwise a 403 with nothing in it.
	Accounts []string
	// Expiry is the token's own expiry, in Unix seconds. It is the *token's*,
	// not the credential's -- see Plugin.Check.
	Expiry int64
}

// decodeClaims reads a JWT payload without verifying it.
//
// Not verifying is deliberate and safe here: the token came from a TLS
// connection to Bandwidth's own token endpoint moments ago, and nothing is
// authorised on the strength of these claims. They are read to say something
// useful on a dashboard -- which accounts this credential reaches -- and a
// claim that cannot be parsed simply goes unreported.
func decodeClaims(token string) claims {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return claims{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims{}
	}
	var raw struct {
		Accounts []json.Number `json:"accounts"`
		Expiry   int64         `json:"exp"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return claims{}
	}
	// Numbers rather than strings, because Bandwidth account ids are numeric
	// and a JSON document is free to write one either way. Normalised here so
	// the rest of the package compares like with like.
	out := claims{Expiry: raw.Expiry}
	for _, a := range raw.Accounts {
		if s := a.String(); s != "" {
			out.Accounts = append(out.Accounts, s)
		}
	}
	return out
}
