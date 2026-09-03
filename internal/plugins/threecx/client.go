package threecx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// maxResponseBody bounds a successful body. One page is at most a hundred
// entities, and the widest of them -- an extension with its forwarding
// profiles expanded -- is a few kilobytes, so this is a backstop against a
// response that is not the API's rather than a limit anything real approaches.
const maxResponseBody = 8 << 20

// Client talks to one phone system as one extension.
type Client struct {
	http    *http.Client
	cfg     Config
	root    string
	log     *slog.Logger
	now     func() time.Time
	limiter *rate.Limiter
	observe func(outcome string, d time.Duration)

	// The credential and the token it buys. Both live only here: the Config
	// the plugin retains has its password blanked, and nothing else in the
	// package can reach either.
	extension string
	password  string

	mu    sync.Mutex
	token string
	until time.Time
}

// NewClient builds a client for one customer's phone system. The credential
// is passed separately from the config so that the Config the plugin retains
// can be free of it.
func NewClient(hc *http.Client, cfg Config, host, extension, password string,
	log *slog.Logger, now func() time.Time,
	observe func(string, time.Duration)) *Client {
	root := rootOf(host)
	return &Client{
		http:      readOnly(hc, root),
		cfg:       cfg,
		root:      root,
		log:       log,
		now:       now,
		limiter:   rate.NewLimiter(rate.Limit(cfg.RequestsPerSecond), 1),
		observe:   observe,
		extension: strings.TrimSpace(extension),
		password:  password,
	}
}

// Describe says where this instance reads from and what its guarantees rest
// on, for the startup log and the health report.
func (c *Client) Describe() string {
	return "the configuration API at " + c.root + " as extension " + c.extension +
		", restricted to a named list of read endpoints by its transport, " +
		"with every read naming its fields"
}

// --- signing in ---------------------------------------------------------------

// loginAnswer is what /webclient/api/Login/GetAccessToken returns.
//
// Status is the part to read: a wrong password is a 401, but a right password
// on an extension that needs a second factor is a 200 with a status that is
// not AuthSuccess and no token.
type loginAnswer struct {
	Status string `json:"Status"`
	Token  struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	} `json:"Token"`
}

// bearer returns a token that is good for at least tokenMargin, signing in
// when there is none or the one held is about to lapse.
//
// One sign-in at a time. Two tool calls arriving together on a cold instance
// would otherwise both send the password, and the PBX counts failed and
// successful sign-ins alike against its anti-hacking limits.
func (c *Client) bearer(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && c.now().Before(c.until.Add(-tokenMargin)) {
		return c.token, nil
	}
	token, life, err := c.login(ctx)
	if err != nil {
		return "", err
	}
	c.token, c.until = token, c.now().Add(life)
	return token, nil
}

// forget drops the held token, so the next call signs in again. Called when
// the PBX answers 401 to a token it issued: a restart or a password change
// invalidates every token, and the right response is one fresh sign-in rather
// than an hour of failures.
func (c *Client) forget(token string) {
	c.mu.Lock()
	if c.token == token {
		c.token, c.until = "", time.Time{}
	}
	c.mu.Unlock()
}

// login exchanges the password for a token.
func (c *Client) login(ctx context.Context) (string, time.Duration, error) {
	if c.extension == "" || c.password == "" {
		return "", 0, fmt.Errorf("3cx: not configured yet -- set the extension " +
			"and password on the Plugins page")
	}
	if err := c.limiter.Wait(ctx); err != nil {
		return "", 0, fmt.Errorf("3cx: waiting to sign in: %w", err)
	}

	body, err := json.Marshal(map[string]string{
		"Username": c.extension, "Password": c.password, "SecurityCode": "",
	})
	if err != nil {
		return "", 0, fmt.Errorf("3cx: preparing the sign-in: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.root+loginPath, bytes.NewReader(body))
	if err != nil {
		return "", 0, fmt.Errorf("3cx: building the sign-in request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	started := c.now()
	resp, err := c.http.Do(req)
	if err != nil {
		c.observe("error", c.now().Sub(started))
		return "", 0, fmt.Errorf("3cx: could not reach %s to sign in: %w", c.root, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	elapsed := c.now().Sub(started)

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		c.observe("error", elapsed)
		return "", 0, fmt.Errorf("3cx: the phone system refused the extension and " +
			"password (HTTP 401); check both on the Plugins page. Repeated " +
			"failures are counted by 3CX's anti-hacking protection, so fix the " +
			"credential before retrying")
	case resp.StatusCode != http.StatusOK:
		c.observe("error", elapsed)
		return "", 0, fmt.Errorf("3cx: signing in failed: %s", summarise(resp.StatusCode, raw))
	}

	var answer loginAnswer
	if err := json.Unmarshal(raw, &answer); err != nil {
		c.observe("error", elapsed)
		return "", 0, fmt.Errorf("3cx: %s answered the sign-in with something that is "+
			"not the phone system's JSON -- the address may be reaching a proxy or "+
			"a different application: %s", c.root, summarise(resp.StatusCode, raw))
	}
	if answer.Status != "AuthSuccess" || answer.Token.AccessToken == "" {
		c.observe("error", elapsed)
		if answer.Status == "" {
			return "", 0, fmt.Errorf("3cx: the sign-in answered without a status, " +
				"so this is probably not a 3CX at that address")
		}
		return "", 0, fmt.Errorf("3cx: the phone system answered the sign-in with %q "+
			"and issued no token. A two-factor code being required is the usual "+
			"cause: this integration cannot supply one, so use an extension "+
			"without 2FA", answer.Status)
	}
	c.observe("ok", elapsed)

	life := time.Duration(answer.Token.ExpiresIn) * time.Second
	if life <= 0 {
		life = fallbackTokenLife
	}
	c.log.DebugContext(ctx, "3cx signed in", "extension", c.extension,
		"token_lifetime", life, "took", elapsed)
	return answer.Token.AccessToken, life, nil
}

// --- reading --------------------------------------------------------------------

// get reads one OData path and decodes the answer.
//
// A 401 is answered once by signing in again: the PBX invalidates every token
// when it restarts or when the extension's password changes, and the held one
// is then wrong through no fault of the configuration. A second 401 is
// reported.
func (c *Client) get(ctx context.Context, path string, q url.Values, into any) error {
	raw, err := c.read(ctx, path, q, true)
	if err != nil {
		return err
	}
	if into == nil {
		return nil
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("3cx: %s answered with JSON in a shape this integration "+
			"does not understand: %w", path, err)
	}
	return nil
}

func (c *Client) read(ctx context.Context, path string, q url.Values, retryAuth bool) (json.RawMessage, error) {
	token, err := c.bearer(ctx)
	if err != nil {
		return nil, err
	}
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("3cx: waiting to read %s: %w", path, err)
	}

	target := c.root + apiPrefix + path
	if encoded := q.Encode(); encoded != "" {
		target += "?" + encoded
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("3cx: building a request for %s: %w", path, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	started := c.now()
	resp, err := c.http.Do(req)
	if err != nil {
		c.observe("error", c.now().Sub(started))
		return nil, fmt.Errorf("3cx: could not reach %s: %w", c.root, err)
	}
	defer resp.Body.Close()

	limit := int64(maxErrorBody)
	if resp.StatusCode == http.StatusOK {
		limit = maxResponseBody
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	elapsed := c.now().Sub(started)
	if err != nil {
		c.observe("error", elapsed)
		return nil, fmt.Errorf("3cx: reading the response from %s: %w", path, err)
	}

	if resp.StatusCode == http.StatusUnauthorized && retryAuth {
		c.observe("error", elapsed)
		c.forget(token)
		c.log.DebugContext(ctx, "3cx token refused; signing in again", "path", path)
		return c.read(ctx, path, q, false)
	}
	if resp.StatusCode != http.StatusOK {
		c.observe("error", elapsed)
		return nil, explainRequestFailure(resp.StatusCode, path, raw)
	}
	c.observe("ok", elapsed)

	// The upstream half of a tool call. Off by default and the first thing to
	// turn on when an assistant reports something that does not match what
	// somebody sees in the 3CX console. Never the body: a successful body here
	// is somebody's staff directory and call records.
	c.log.DebugContext(ctx, "3cx API call", "path", path, "status", resp.StatusCode,
		"bytes", len(raw), "took", elapsed)
	return raw, nil
}

// page is one OData collection response.
type page[T any] struct {
	Count *int `json:"@odata.count"`
	Value []T  `json:"value"`
}

// listing is what a paged read produces: the rows, how many the PBX holds in
// total when it said, and whether the walk stopped short of them.
type listing[T any] struct {
	Rows []T
	// Total is the collection's size as the PBX reports it with $count, or -1
	// when it did not say.
	Total int
	// Truncated reports that more rows exist than were fetched.
	Truncated bool
}

// list walks a collection page by page up to max rows.
//
// 3CX refuses any $top above 100, so a phone system with more extensions than
// that has to be paged through; asking for 500 in one go is a 400 on every
// site with a real number of phones. $count is asked for on the first page so
// a listing can say how many there are rather than how many it fetched.
func list[T any](ctx context.Context, c *Client, path string, q url.Values, max int) (listing[T], error) {
	out := listing[T]{Total: -1}
	if max <= 0 {
		max = c.cfg.MaxItems
	}
	q = cloneValues(q)
	for skip := 0; len(out.Rows) < max; skip += pageSize {
		want := min(pageSize, max-len(out.Rows))
		q.Set("$top", fmt.Sprint(want))
		q.Set("$skip", fmt.Sprint(skip))
		if skip == 0 {
			q.Set("$count", "true")
		} else {
			q.Del("$count")
		}
		var p page[T]
		if err := c.get(ctx, path, q, &p); err != nil {
			return listing[T]{}, err
		}
		if skip == 0 && p.Count != nil {
			out.Total = *p.Count
		}
		out.Rows = append(out.Rows, p.Value...)
		if len(p.Value) < want {
			return out, nil
		}
	}
	// Full up. Whether more exist is known from the count where the PBX gave
	// one; otherwise one more row would have to be asked for to be sure, and
	// saying "possibly more" honestly is cheaper than another round trip.
	if out.Total >= 0 {
		out.Truncated = out.Total > len(out.Rows)
	} else {
		out.Truncated = true
	}
	return out, nil
}

// one reads a single entity by a filter and reports whether it existed.
func one[T any](ctx context.Context, c *Client, path string, q url.Values) (T, bool, error) {
	var zero T
	q = cloneValues(q)
	q.Set("$top", "1")
	var p page[T]
	if err := c.get(ctx, path, q, &p); err != nil {
		return zero, false, err
	}
	if len(p.Value) == 0 {
		return zero, false, nil
	}
	return p.Value[0], true, nil
}

func cloneValues(q url.Values) url.Values {
	out := make(url.Values, len(q)+3)
	for k, v := range q {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// Probe checks the address, the credential and the role, in that order,
// because they fail differently and the difference is the whole value of
// probing at all.
//
// Signing in proves the address resolves, TLS works, the thing answering is a
// 3CX and the password is accepted. The extension list, one row, then proves
// the role: a normal extension signs in perfectly well and is refused every
// listing with a 403, which would otherwise surface inside the first tool call
// an assistant makes rather than on the dashboard where somebody can fix it.
func (c *Client) Probe(ctx context.Context) (systemInfo, error) {
	if _, err := c.bearer(ctx); err != nil {
		return systemInfo{}, err
	}
	var info systemInfo
	q := url.Values{"$select": {"FQDN,Version,ExtensionsTotal,TrunksTotal"}}
	if err := c.get(ctx, "SystemStatus", q, &info); err != nil {
		return systemInfo{}, err
	}
	probe := url.Values{"$select": {"Id"}, "$top": {"1"}}
	if err := c.get(ctx, "Users", probe, nil); err != nil {
		return systemInfo{}, err
	}
	return info, nil
}

// systemInfo is what the probe learns, for the startup log.
type systemInfo struct {
	FQDN            string `json:"FQDN"`
	Version         string `json:"Version"`
	ExtensionsTotal int    `json:"ExtensionsTotal"`
	TrunksTotal     int    `json:"TrunksTotal"`
}

// maxBundle is the largest support bundle that will be read. Real ones run
// from a few megabytes to forty; a hundred leaves room for a big site without
// letting one phone system spend all the memory this process has.
const maxBundle = 100 << 20

// fetchBundle downloads a support bundle.
//
// The PBX builds the zip on request, walking its logs first, which on a large
// site takes minutes -- so this ignores the client's ordinary timeout and lets
// the caller's context bound it. Held in memory rather than spooled to disk: a
// zip needs random access to be read at all, and a temporary file of
// somebody's logs is a thing to clean up and eventually fail to.
func (c *Client) fetchBundle(ctx context.Context) ([]byte, error) {
	token, err := c.bearer(ctx)
	if err != nil {
		return nil, err
	}
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("3cx: waiting to collect the bundle: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.root+apiPrefix+"SupportInfo", nil)
	if err != nil {
		return nil, fmt.Errorf("3cx: building the bundle request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/zip, application/octet-stream")

	patient := *c.http
	patient.Timeout = 0
	started := c.now()
	resp, err := patient.Do(req)
	if err != nil {
		c.observe("error", c.now().Sub(started))
		return nil, fmt.Errorf("3cx: could not reach %s for the bundle: %w", c.root, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		c.observe("error", c.now().Sub(started))
		if resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("3cx: this phone system does not offer a support bundle " +
				"over the API (HTTP 404); it may be an older build than v20")
		}
		return nil, explainRequestFailure(resp.StatusCode, "SupportInfo", raw)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBundle+1))
	elapsed := c.now().Sub(started)
	if err != nil {
		c.observe("error", elapsed)
		return nil, fmt.Errorf("3cx: reading the bundle: %w", err)
	}
	if len(raw) > maxBundle {
		c.observe("error", elapsed)
		return nil, fmt.Errorf("3cx: the support bundle is larger than %d MB, which is more "+
			"than this integration will hold in memory", maxBundle>>20)
	}
	c.observe("ok", elapsed)
	c.log.DebugContext(ctx, "3cx support bundle collected", "bytes", len(raw), "took", elapsed)
	return raw, nil
}
