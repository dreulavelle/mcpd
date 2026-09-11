package threecx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
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
		return "", 0, c.explainTransport(err, signingIn)
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
		return nil, c.explainTransport(err, "waiting to read "+path)
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
		return nil, c.explainTransport(err, "reading "+path)
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
		// Through the same explanation as a failed Do: the client's timeout
		// covers the body as well as the headers, and a large page whose
		// headers arrived and whose body did not is the exact failure this is
		// about.
		return nil, c.explainTransport(err, "reading the response from "+path)
	}

	if resp.StatusCode == http.StatusUnauthorized && retryAuth {
		c.observe("error", elapsed)
		c.forget(token)
		c.log.DebugContext(ctx, "3cx token refused; signing in again", "path", path)
		return c.read(ctx, path, q, false)
	}
	if resp.StatusCode != http.StatusOK {
		c.observe("error", elapsed)
		// Logged here as well as returned. A refusal used to return before the
		// line below, so debug logging -- the thing somebody turns on to find
		// out what the phone system was asked -- recorded every call that
		// worked and nothing about the one that did not, and a health warning
		// naming a failed read had no request behind it anywhere. The body is
		// left out for the same reason it is left out of a success: it is the
		// upstream's, and its sentence is already in the error the caller gets.
		c.log.DebugContext(ctx, "3cx API call refused", "path", path,
			"status", resp.StatusCode, "took", elapsed)
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

// explainTransport turns a request that never came back into a sentence.
//
// A timeout is the one worth telling apart. On a large installation a wide
// question genuinely takes longer than the default, and the error a caller saw
// was "could not reach https://...: context deadline exceeded", which reads as
// the phone system being down and has nobody reaching for the setting that
// fixes it.
func (c *Client) explainTransport(err error, what string) error {
	var ue *url.Error
	ours := errors.As(err, &ue) && ue.Timeout()
	if !ours && !errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("3cx: could not reach %s while %s: %w", c.root, what, err)
	}
	// Only our own timeout knows what it waited. A deadline set further up --
	// a caller with less time than this client's setting, or the support
	// bundle's own -- would otherwise be reported as a wait that never
	// elapsed, pointing at a setting that would not have helped.
	waited := "the request ran out of time"
	if ours {
		waited = fmt.Sprintf("it did not answer within %s", c.cfg.Timeout())
	}
	switch what {
	case collectingBundle:
		// No setting to raise: a collection is bounded by the job's own
		// deadline, and what an operator can do is make the bundle smaller or
		// take it by hand.
		return fmt.Errorf("3cx: %s did not finish building and sending its support "+
			"bundle in time. A system with debug logging or a packet capture running "+
			"builds a much larger one; turn those off and ask again, or collect it in "+
			"the 3CX console", c.root)
	case signingIn:
		// No rows to ask fewer of, so only the half of the advice that applies.
		return fmt.Errorf("3cx: %s while signing in: %s. Raise how long to wait for an "+
			"answer on the mcpd Plugins page if the phone system is simply slow to reach",
			c.root, waited)
	}
	return fmt.Errorf("3cx: %s while %s: %s. A large phone system can take longer than "+
		"that on a wide question: ask for fewer rows, a shorter time window or one "+
		"extension, or raise how long to wait for an answer on the mcpd Plugins page",
		c.root, what, waited)
}

// The two things this explanation is shared with that are not reads, and where
// "ask for fewer rows" would be advice about nothing. The bundle has no
// timeout of its own on the client -- the job's deadline bounds it -- so what
// it needs said is which phase ran out, not which setting to raise.
const (
	signingIn        = "signing in"
	collectingBundle = "collecting the support bundle"
)

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
	// Truncated reports that the walk stopped short of what the phone system
	// holds -- for certain when a count was asked for, and otherwise only
	// because it filled up.
	Truncated bool
}

// reason says why a listing stopped short, in the words a caller is given, or
// nothing when it did not. How sure it is depends on whether a count was asked
// for, and the difference matters to a model deciding whether to ask again.
func (l listing[T]) reason() string {
	switch {
	case !l.Truncated:
		return ""
	case l.Total >= 0:
		return reasonCount
	}
	return reasonMaybeCount
}

// list walks a collection page by page up to max rows.
//
// 3CX refuses any $top above 100, so a phone system with more extensions than
// that has to be paged through; asking for 500 in one go is a 400 on every
// site with a real number of phones.
//
// It does not ask how many there are. $count=true makes the phone system count
// the whole collection before it answers the first page, and on a view with
// millions of rows behind it -- call history on a busy site -- that count is
// most of the time the request takes, and it timed out queries that would
// otherwise have returned fifty rows in a second. listCounted is for the two
// listings that actually report a total.
func list[T any](ctx context.Context, c *Client, path string, q url.Values, max int) (listing[T], error) {
	return walk[T](ctx, c, path, q, max, false)
}

// listCounted walks a collection and asks the phone system how many rows it
// holds in total, so a listing can say how many there are rather than how many
// it fetched. Only for a listing that reports the number: the count is paid
// for on the first page whether or not anything reads it.
func listCounted[T any](ctx context.Context, c *Client, path string, q url.Values, max int) (listing[T], error) {
	return walk[T](ctx, c, path, q, max, true)
}

func walk[T any](ctx context.Context, c *Client, path string, q url.Values, max int, count bool) (listing[T], error) {
	out := listing[T]{Total: -1}
	if max <= 0 {
		max = c.cfg.MaxItems
	}
	q = cloneValues(q)
	for skip := 0; len(out.Rows) < max; skip += pageSize {
		want := min(pageSize, max-len(out.Rows))
		q.Set("$top", fmt.Sprint(want))
		q.Set("$skip", fmt.Sprint(skip))
		if count && skip == 0 {
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
	// Full up. Whether more exist is known from the count where one was asked
	// for; otherwise one more row would have to be asked for to be sure, and
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
// from a few megabytes to a few hundred on a busy site with a packet capture
// in them; a gigabyte is a phone system whose logs are themselves the fault.
// It is a ceiling on the data volume rather than on memory: the download is
// spooled to a file.
const maxBundle int64 = 1 << 30

// progressStep is how much has to land before a download says so again.
// Progress is read by somebody polling a job, not by a meter, so a few
// megabytes between updates is plenty and costs one lock instead of one per
// read.
const progressStep = 4 << 20

// downloadBundle streams the support bundle to a file in dir and hands the
// file back, together with its size.
//
// Spooled rather than held. The PBX builds the zip on request, walking its
// logs first, which on a large site takes minutes -- so this ignores the
// client's ordinary timeout and lets the caller's context bound it -- and what
// comes back is hundreds of megabytes on a system with a capture in it. Held
// in memory that was refused above a hundred megabytes, which is a size a
// production system reaches without anything being wrong; on disk it is a file
// the kernel reclaims.
//
// ceiling is the largest bundle that will be spooled; progress is called as
// the download lands, and once with what the phone system said it was sending
// if it said. The caller closes the file.
func (c *Client) downloadBundle(ctx context.Context, dir string, ceiling int64, progress func(got, expected int64)) (*os.File, int64, error) {
	token, err := c.bearer(ctx)
	if err != nil {
		return nil, 0, err
	}
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, 0, c.explainTransport(err, collectingBundle)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.root+apiPrefix+"SupportInfo", nil)
	if err != nil {
		return nil, 0, fmt.Errorf("3cx: building the bundle request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/zip, application/octet-stream")

	patient := *c.http
	patient.Timeout = 0
	started := c.now()
	resp, err := patient.Do(req)
	if err != nil {
		c.observe("error", c.now().Sub(started))
		return nil, 0, c.explainTransport(err, collectingBundle)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		c.observe("error", c.now().Sub(started))
		if resp.StatusCode == http.StatusNotFound {
			return nil, 0, fmt.Errorf("3cx: this phone system does not offer a support bundle " +
				"over the API (HTTP 404); it may be an older build than v20")
		}
		return nil, 0, explainRequestFailure(resp.StatusCode, "SupportInfo", raw)
	}

	// Where the phone system says how much it is sending, a bundle past the
	// ceiling is refused before a byte of it lands rather than after a
	// gigabyte of it has.
	expected := resp.ContentLength
	if expected > ceiling {
		c.observe("error", c.now().Sub(started))
		return nil, 0, tooLarge(ceiling)
	}
	f, err := spoolFile(dir)
	if err != nil {
		c.observe("error", c.now().Sub(started))
		return nil, 0, err
	}
	if progress != nil {
		progress(0, expected)
	}
	// One byte past the ceiling, so a bundle that is exactly at it is read and
	// one over it is refused with the size named rather than silently cut.
	landed := &counter{w: f, expected: expected, fn: progress}
	n, err := io.Copy(landed, io.LimitReader(resp.Body, ceiling+1))
	elapsed := c.now().Sub(started)
	if err != nil {
		c.observe("error", elapsed)
		closeSpool(f)
		// The two ends of the copy fail for different reasons and are fixed in
		// different places: one is the phone system or the network, the other
		// is this host's disk, and an operator told "reading the bundle: no
		// space left on device" looks at the wrong machine first.
		if landed.err != nil {
			return nil, 0, fmt.Errorf("3cx: could not write the support bundle to %s: %w. "+
				"The bundle is spooled there while it is read, and it can be as large as "+
				"the phone system's logs; free some space on the data volume", spoolDir(dir), landed.err)
		}
		return nil, 0, fmt.Errorf("3cx: reading the bundle: %w", err)
	}
	if n > ceiling {
		c.observe("error", elapsed)
		closeSpool(f)
		return nil, 0, tooLarge(ceiling)
	}
	if progress != nil {
		progress(n, expected)
	}
	c.observe("ok", elapsed)
	c.log.DebugContext(ctx, "3cx support bundle collected", "bytes", n, "took", elapsed)
	return f, n, nil
}

// tooLarge is the refusal for a bundle past the ceiling. It says what to do,
// because "too large" on its own leaves somebody with a phone system they
// cannot read at all.
func tooLarge(ceiling int64) error {
	return fmt.Errorf("3cx: the support bundle is larger than %s, which is more than this "+
		"integration will spool to disk. Take a fresh one with the packet capture and the "+
		"debug logs turned off, or read it in the 3CX console", sizeText(ceiling))
}

// sizeText renders a byte count the way somebody says it out loud.
func sizeText(n int64) string {
	switch {
	case n >= 1<<30 && n%(1<<30) == 0:
		return fmt.Sprintf("%d GB", n>>30)
	case n >= 1<<20:
		return fmt.Sprintf("%d MB", n>>20)
	}
	return fmt.Sprintf("%d KB", max(n>>10, 1))
}

// spoolDir names the directory a download is spooled to, for a message about
// it failing.
func spoolDir(dir string) string {
	if strings.TrimSpace(dir) == "" {
		return os.TempDir()
	}
	return dir
}

// spoolFile opens the file a download lands in.
//
// Unlinked as soon as it exists: the handle stays good, and the space goes
// back when it closes -- including when this process is killed mid-download,
// which is the case a deferred remove does not cover. On a system that refuses
// to unlink an open file the name survives and the caller's remove covers it.
func spoolFile(dir string) (*os.File, error) {
	if dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("3cx: could not open %s to spool the support bundle: %w", dir, err)
		}
	}
	f, err := os.CreateTemp(dir, "threecx-bundle-*.zip")
	if err != nil {
		return nil, fmt.Errorf("3cx: could not open a file to spool the support bundle: %w", err)
	}
	_ = os.Remove(f.Name())
	return f, nil
}

// closeSpool discards a spooled download.
func closeSpool(f *os.File) {
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
}

// counter is the writer a download lands through, so somebody polling the job
// can be told how far it has got.
type counter struct {
	w        io.Writer
	fn       func(got, expected int64)
	expected int64
	got      int64
	told     int64
	// err is the write's own failure, kept because io.Copy hands back one
	// error for both ends and the two are fixed in different places.
	err error
}

func (c *counter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if err != nil {
		c.err = err
	}
	c.got += int64(n)
	if c.fn != nil && c.got-c.told >= progressStep {
		c.told = c.got
		c.fn(c.got, c.expected)
	}
	return n, err
}
