package cnmaestro

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

// Client talks to the cnMaestro v2 API.
//
// Three things it does that a naive client would not, each because the API
// makes the naive choice wrong:
//
//   - Every request path is checked against the deny-list, so no caller can
//     reach the remote-command endpoints even by constructing a path directly.
//   - managed_account is attached to every request, because omitting it means
//     different things depending on whether the request names a network.
//   - Pagination follows continuation tokens, since offset is deprecated and
//     removed in 6.4.0.
type Client struct {
	http    *http.Client
	tokens  *tokenManager
	limiter *rate.Limiter
	cfg     Config
	log     *slog.Logger
}

// NewClient builds an API client.
func NewClient(httpClient *http.Client, cfg Config, clientID, secret string, log *slog.Logger, now func() time.Time) *Client {
	cfg.withDefaults()
	return &Client{
		http:    httpClient,
		tokens:  newTokenManager(httpClient, cfg.BaseURL, clientID, secret, now),
		limiter: rate.NewLimiter(rate.Limit(cfg.RequestsPerSecond), cfg.Burst),
		cfg:     cfg,
		log:     log,
	}
}

// Envelope is the shape every list response shares.
type Envelope struct {
	Paging   Paging          `json:"paging"`
	Warnings []string        `json:"warnings,omitempty"`
	Data     json.RawMessage `json:"data"`
}

// Paging carries both the legacy offset fields and the continuation token that
// replaces them in 6.4.0.
type Paging struct {
	Offset                int    `json:"offset"`
	Limit                 int    `json:"limit"`
	Total                 int    `json:"total"`
	Status                string `json:"status,omitempty"`
	NextContinuationToken string `json:"next_continuation_token,omitempty"`
}

// Get performs a read and decodes the envelope.
func (c *Client) Get(ctx context.Context, path string, params url.Values) (*Envelope, error) {
	body, err := c.do(ctx, http.MethodGet, path, params, nil)
	if err != nil {
		return nil, err
	}
	var env Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("cnmaestro: decode response from %s: %w", path, err)
	}
	if len(env.Warnings) > 0 {
		// A 200 can carry partial-failure detail. Discarding it would let a
		// tool report a complete answer built from incomplete data.
		c.log.Warn("cnmaestro returned warnings with a successful response",
			"path", path, "warnings", env.Warnings)
	}
	return &env, nil
}

// GetInto reads a single resource and decodes its data payload.
func (c *Client) GetInto(ctx context.Context, path string, params url.Values, into any) error {
	env, err := c.Get(ctx, path, params)
	if err != nil {
		return err
	}
	if len(env.Data) == 0 {
		return ErrDeviceNotFound
	}
	// Single-resource endpoints return a one-element array.
	if bytes.HasPrefix(bytes.TrimSpace(env.Data), []byte("[")) {
		var items []json.RawMessage
		if err := json.Unmarshal(env.Data, &items); err != nil {
			return fmt.Errorf("cnmaestro: decode %s: %w", path, err)
		}
		if len(items) == 0 {
			return ErrDeviceNotFound
		}
		return json.Unmarshal(items[0], into)
	}
	return json.Unmarshal(env.Data, into)
}

// List walks a paginated collection, appending each page's raw items.
//
// It follows continuation tokens rather than offsets. Offset pagination is
// deprecated upstream and removed in 6.4.0, and it is also unsound while an
// estate is changing: rows shift between pages as devices come and go.
func (c *Client) List(ctx context.Context, path string, params url.Values) ([]json.RawMessage, Paging, error) {
	if params == nil {
		params = url.Values{}
	}
	params.Set("limit", strconv.Itoa(c.cfg.PageSize))

	var (
		all      []json.RawMessage
		lastPage Paging
		token    string
	)

	for page := range c.cfg.MaxPages {
		q := cloneValues(params)
		if token != "" {
			q.Set("continuation_token", token)
		}

		env, err := c.Get(ctx, path, q)
		if err != nil {
			return nil, lastPage, err
		}
		lastPage = env.Paging

		var items []json.RawMessage
		if len(env.Data) > 0 {
			if err := json.Unmarshal(env.Data, &items); err != nil {
				return nil, lastPage, fmt.Errorf("cnmaestro: decode page %d of %s: %w", page, path, err)
			}
		}
		all = append(all, items...)

		token = env.Paging.NextContinuationToken
		if token == "" || len(items) == 0 {
			return all, lastPage, nil
		}
	}

	// Truncation is reported rather than hidden. A silently capped list reads
	// as a complete answer, and a model acting on it would conclude a device
	// does not exist when it simply fell past the cap.
	c.log.Warn("paginated read hit its page cap; results are incomplete",
		"path", path, "pages", c.cfg.MaxPages, "items", len(all), "total", lastPage.Total)
	return all, lastPage, nil
}

// do performs one request with rate limiting, authentication, and the checks
// that make this client safe to expose to a model.
func (c *Client) do(ctx context.Context, method, path string, params url.Values, body any) ([]byte, error) {
	// The deny-list is enforced here, below every caller, so that no tool or
	// mutation can reach a blocked endpoint even by building a path directly.
	if err := checkPath(path); err != nil {
		return nil, err
	}

	if err := c.limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("cnmaestro: rate limiter: %w", err)
	}

	token, err := c.tokens.Token(ctx)
	if err != nil {
		return nil, err
	}

	q := cloneValues(params)
	// Always explicit. The API's default depends on whether the request names
	// a network, so leaving it off makes the tenant scope of a call depend on
	// its other parameters.
	if q.Get("managed_account") == "" {
		q.Set("managed_account", c.cfg.ManagedAccount)
	}

	endpoint := c.tokens.APIHost() + "/api/v2" + ensureLeadingSlash(path)
	if encoded := q.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}

	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("cnmaestro: encode request body: %w", err)
		}
		reader = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, fmt.Errorf("cnmaestro: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cnmaestro: %s %s: %w", method, path, scrubURLError(err))
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("cnmaestro: read response from %s: %w", path, err)
	}

	if resp.StatusCode >= 400 {
		return nil, &APIError{
			StatusCode: resp.StatusCode,
			Method:     method,
			Path:       path,
			Message:    extractMessage(respBody),
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
		}
	}
	return respBody, nil
}

// Post performs a write.
func (c *Client) Post(ctx context.Context, path string, body any) ([]byte, error) {
	return c.do(ctx, http.MethodPost, path, nil, body)
}

// Put performs an update.
func (c *Client) Put(ctx context.Context, path string, body any) ([]byte, error) {
	return c.do(ctx, http.MethodPut, path, nil, body)
}

// Ping verifies credentials and reachability for the health check.
func (c *Client) Ping(ctx context.Context) error {
	params := url.Values{"limit": {"1"}}
	_, err := c.Get(ctx, "/devices", params)
	return err
}

// extractMessage pulls a usable message from an error body without letting an
// HTML error page into a log line or an audit record.
func extractMessage(body []byte) string {
	const maxLen = 256

	var payload struct {
		Message string `json:"message"`
		Error   string `json:"error"`
		Detail  string `json:"detail"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		for _, candidate := range []string{payload.Message, payload.Error, payload.Detail} {
			if candidate != "" {
				return truncateString(candidate, maxLen)
			}
		}
	}

	text := strings.TrimSpace(string(body))
	// An HTML body carries no useful message and a great deal of noise.
	if strings.HasPrefix(text, "<") {
		return ""
	}
	return truncateString(text, maxLen)
}

func truncateString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func parseRetryAfter(v string) int {
	if v == "" {
		return 0
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs < 0 {
		return 0
	}
	return secs
}

func cloneValues(v url.Values) url.Values {
	out := make(url.Values, len(v))
	for k, vals := range v {
		out[k] = append([]string(nil), vals...)
	}
	return out
}

func ensureLeadingSlash(p string) string {
	if strings.HasPrefix(p, "/") {
		return p
	}
	return "/" + p
}
