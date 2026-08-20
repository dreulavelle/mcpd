package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// clientMetadata is the RFC 7591 registration document, and also the shape a
// Client ID Metadata Document serves.
type clientMetadata struct {
	ClientID                string   `json:"client_id,omitempty"`
	ClientName              string   `json:"client_name,omitempty"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types,omitempty"`
	ResponseTypes           []string `json:"response_types,omitempty"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method,omitempty"`
	Scope                   string   `json:"scope,omitempty"`
	ClientSecret            string   `json:"client_secret,omitempty"`
	ClientIDIssuedAt        int64    `json:"client_id_issued_at,omitempty"`
	RegistrationAccessToken string   `json:"registration_access_token,omitempty"`
}

// handleRegister implements RFC 7591 dynamic client registration.
//
// Registration is open, which is the point: ChatGPT registers itself with no
// operator involvement. That is safe here because registering confers nothing
// on its own. A client still cannot obtain a token without a user completing
// the consent flow, and the token it receives is bounded by that user's own
// role and plugin grants.
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req clientMetadata
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		writeOAuthError(w, errInvalidRequest("registration document is not valid JSON"))
		return
	}
	if err := validateRedirectURIs(req.RedirectURIs); err != nil {
		writeOAuthError(w, errInvalidRequest(err.Error()))
		return
	}

	clientID, err := NewID("cli_")
	if err != nil {
		writeOAuthError(w, errServer("could not allocate a client id"))
		return
	}

	client := &Client{
		ID:           clientID,
		Name:         sanitizeDisplay(req.ClientName),
		RedirectURIs: req.RedirectURIs,
		Type:         RegDynamic,
	}

	resp := clientMetadata{
		ClientID:         clientID,
		ClientName:       client.Name,
		RedirectURIs:     client.RedirectURIs,
		GrantTypes:       []string{"authorization_code", "refresh_token"},
		ResponseTypes:    []string{"code"},
		ClientIDIssuedAt: s.now().Unix(),
	}

	// A client asking for "none" is public and relies on PKCE, which every
	// client must use here regardless. Anything else gets a secret.
	if req.TokenEndpointAuthMethod == "none" {
		resp.TokenEndpointAuthMethod = "none"
	} else {
		secret, err := GenerateSecret()
		if err != nil {
			writeOAuthError(w, errServer("could not allocate a client secret"))
			return
		}
		client.SecretHash = HashSecret(secret)
		resp.ClientSecret = secret
		resp.TokenEndpointAuthMethod = "client_secret_post"
	}

	if err := s.store.UpsertClient(r.Context(), client, ""); err != nil {
		s.log.Error("failed to persist client registration", "error", err)
		writeOAuthError(w, errServer("could not persist the registration"))
		return
	}

	s.log.Info("client registered",
		"client_id", clientID, "name", client.Name,
		"public", client.IsPublic(), "redirect_uris", client.RedirectURIs)
	writeJSON(w, http.StatusCreated, resp)
}

// validateRedirectURIs enforces the rules that keep the authorization endpoint
// from becoming an open redirector.
func validateRedirectURIs(uris []string) error {
	if len(uris) == 0 {
		return fmt.Errorf("redirect_uris must contain at least one entry")
	}
	if len(uris) > 16 {
		return fmt.Errorf("redirect_uris must contain at most 16 entries")
	}
	for _, raw := range uris {
		u, err := url.Parse(raw)
		if err != nil {
			return fmt.Errorf("redirect_uri %q is not a valid URL", raw)
		}
		if u.Fragment != "" {
			// RFC 6749 section 3.1.2: fragments are forbidden, and a fragment
			// would in any case not survive the redirect.
			return fmt.Errorf("redirect_uri %q must not contain a fragment", raw)
		}
		switch u.Scheme {
		case "https":
			if u.Host == "" {
				return fmt.Errorf("redirect_uri %q has no host", raw)
			}
		case "http":
			// Plaintext is permitted only for loopback, where there is no
			// network to intercept. This is what lets a desktop client
			// register 127.0.0.1 while a hosted one cannot register http://.
			if !isLoopback(u.Hostname()) {
				return fmt.Errorf("redirect_uri %q must use https unless it is a loopback address", raw)
			}
		default:
			// Private-use schemes (com.example.app:/callback) are how native
			// apps receive a redirect and are permitted, but they must carry
			// a dotted, reversed-domain scheme rather than a bare word.
			if !strings.Contains(u.Scheme, ".") {
				return fmt.Errorf("redirect_uri %q uses an unsupported scheme %q", raw, u.Scheme)
			}
		}
	}
	return nil
}

func isLoopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "[::1]"
}

// sanitizeDisplay bounds and cleans a client-supplied name before it is shown
// on the consent screen. The name is attacker-controlled text rendered to a
// user about to grant access, so it must not be able to impersonate the
// surrounding page.
func sanitizeDisplay(s string) string {
	const maxLen = 64
	var b strings.Builder
	for _, r := range s {
		if b.Len() >= maxLen {
			break
		}
		// Printable ASCII only. Control characters, newlines, and
		// direction-override characters all have a role in spoofing.
		if r >= 0x20 && r < 0x7f {
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "Unnamed client"
	}
	return out
}

// --- Client ID Metadata Documents ------------------------------------------

// cimdFetcher resolves a client_id that is an HTTPS URL to the metadata
// document it serves. CIMD supersedes dynamic registration in the 2026-07-28
// MCP revision.
type cimdFetcher struct {
	client *http.Client
}

func newCIMDFetcher(c *http.Client) *cimdFetcher {
	if c == nil {
		c = &http.Client{Timeout: 10 * time.Second}
	}
	return &cimdFetcher{client: c}
}

// Fetch retrieves and validates a Client ID Metadata Document.
func (f *cimdFetcher) Fetch(ctx context.Context, clientID string) (*Client, error) {
	u, err := url.Parse(clientID)
	if err != nil {
		return nil, fmt.Errorf("oauth: client_id is not a URL: %w", err)
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("oauth: a client id metadata document must be served over https")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clientID, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oauth: fetch client metadata: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oauth: client metadata returned %d", resp.StatusCode)
	}

	var meta clientMetadata
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&meta); err != nil {
		return nil, fmt.Errorf("oauth: client metadata is not valid JSON: %w", err)
	}

	// The document must claim the identity it was fetched from. Without this
	// check, one site could serve metadata asserting another's client_id.
	if meta.ClientID != clientID {
		return nil, fmt.Errorf("oauth: client metadata declares client_id %q but was fetched from %q",
			meta.ClientID, clientID)
	}
	if err := validateRedirectURIs(meta.RedirectURIs); err != nil {
		return nil, fmt.Errorf("oauth: client metadata: %w", err)
	}

	return &Client{
		ID:           clientID,
		Name:         sanitizeDisplay(meta.ClientName),
		RedirectURIs: meta.RedirectURIs,
		Type:         RegCIMD,
	}, nil
}
