package mcpremote

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/time/rate"
)

// dialTimeout bounds establishing a session, including the MCP initialize
// round trip. Generous enough for a cold serverless upstream, short enough
// that a health check does not become the thing that hangs.
const dialTimeout = 20 * time.Second

// conn owns the session to one remote server.
//
// A session is established lazily and shared. Lazily because Register must not
// touch the network and Start must not fail the mount when the far end is
// down; shared because a session carries an id the far end tracks, and one per
// call would leave a trail of abandoned sessions behind every burst.
type conn struct {
	endpoint string
	client   *http.Client
	impl     *mcp.Implementation

	mu      sync.Mutex
	current *mcp.ClientSession
	// pending is the dial in flight, if there is one. Callers join it rather
	// than starting their own, so an unreachable server costs one dial at a
	// time however many callers are waiting.
	pending *dial
	// closed stops a dial after Shutdown, so a call arriving during teardown
	// cannot open a session nothing will close.
	closed bool
}

// dial is one attempt to establish a session, running on its own goroutine so
// that no caller's deadline is anyone else's and no caller waits on a mutex
// held across a TCP connect.
//
// The mutex is the part that mattered: holding it across the dial meant an
// unreachable server stalled close() behind a twenty-second connect, so
// shutdown overran its bounded context.
type dial struct {
	done    chan struct{}
	cancel  context.CancelFunc
	session *mcp.ClientSession
	err     error
}

func newConn(endpoint string, headers map[string]string, impl *mcp.Implementation) *conn {
	return &conn{endpoint: endpoint, client: newHTTPClient(headers), impl: impl}
}

// session returns a live session, dialling if there is not one.
//
// The caller waits for whichever comes first: the dial finishing, or its own
// context ending. That second half is what keeps the readiness probe honest --
// it runs on a two-second budget, and before this it waited out the full dial
// timeout for every unreachable server in turn, so an orchestrator restarted a
// host that was serving perfectly well.
//
// A caller that gives up does not cancel the dial. It is shared, and the next
// caller -- or the next health check -- collects the result.
func (c *conn) session(ctx context.Context) (*mcp.ClientSession, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errors.New("this server is shutting down")
	}
	if c.current != nil {
		session := c.current
		c.mu.Unlock()
		return session, nil
	}
	pending := c.pending
	if pending == nil {
		pending = c.beginDialLocked(ctx)
	}
	c.mu.Unlock()

	select {
	case <-ctx.Done():
		// Reported as the caller running out of time rather than as the server
		// being unreachable, because those are different things and only one
		// of them is the far end's fault.
		return nil, fmt.Errorf("gave up waiting to connect: %w", ctx.Err())
	case <-pending.done:
		if pending.err != nil {
			return nil, pending.err
		}
		return pending.session, nil
	}
}

// beginDialLocked starts a dial. c.mu must be held; it is not held while the
// dial runs.
func (c *conn) beginDialLocked(ctx context.Context) *dial {
	// Detached from the caller's cancellation, and bounded on its own. The
	// session outlives the call that happened to open it, so one caller
	// changing its mind must not cancel a handshake several others are waiting
	// on. Values are kept, which is what carries the correlation id into the
	// transport's logs.
	dialCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dialTimeout)

	d := &dial{done: make(chan struct{}), cancel: cancel}
	c.pending = d

	go func() {
		defer cancel()

		client := mcp.NewClient(c.impl, nil)
		transport := &mcp.StreamableClientTransport{
			Endpoint:   c.endpoint,
			HTTPClient: c.client,
			// This host does not act on server-initiated messages, and the
			// standalone stream is a connection held open per server for the
			// life of the process to receive them. Discovery is explicit here
			// -- an administrator asks for it, and classifies what comes back
			// -- so a tools/list_changed notification would have nowhere
			// useful to go.
			DisableStandaloneSSE: true,
		}
		session, err := client.Connect(dialCtx, transport, nil)

		c.mu.Lock()
		d.session, d.err = session, err
		c.pending = nil
		switch {
		case err != nil:
			// Nothing to keep.
		case c.closed:
			// Shutdown happened while this was in flight. The session is
			// nobody's, so it is closed here rather than left open.
			d.session, d.err = nil, errors.New("this server is shutting down")
		default:
			c.current = session
		}
		c.mu.Unlock()

		if session != nil && d.session == nil {
			_ = session.Close()
		}
		close(d.done)
	}()

	return d
}

// drop closes and forgets the session, so the next call dials again.
//
// Called on any transport-level failure. A tool that returns an error does so
// inside a successful result; an error out of CallTool means the conversation
// itself broke, and reusing a broken session just fails the next call too.
func (c *conn) drop() {
	c.mu.Lock()
	session := c.current
	c.current = nil
	c.mu.Unlock()
	if session != nil {
		_ = session.Close()
	}
}

// close tears the session down for good.
//
// It cancels a dial in flight rather than waiting for one. Shutdown runs on a
// bounded context, and a remote server that is not answering is exactly the
// case where the dial would still be running.
func (c *conn) close() error {
	c.mu.Lock()
	session := c.current
	pending := c.pending
	c.current = nil
	c.closed = true
	c.mu.Unlock()

	if pending != nil {
		pending.cancel()
	}
	if session == nil {
		return nil
	}
	return session.Close()
}

// ping establishes a session if needed and checks the far end answers.
func (c *conn) ping(ctx context.Context) error {
	session, err := c.session(ctx)
	if err != nil {
		return err
	}
	if err := session.Ping(ctx, nil); err != nil {
		c.drop()
		return err
	}
	return nil
}

// newHTTPClient builds the client this transport needs.
//
// Deliberately not the host's shared plugin client. That one carries a flat
// 30-second Timeout, which is correct for a request/response API and wrong
// here: http.Client.Timeout covers reading the body, and a streamable-http
// response body is a stream that may legitimately stay open while a slow tool
// works. A call that should be bounded is bounded by its own context, which is
// where the bound belongs.
//
// The transport-level timeouts remain, because they bound the things that
// genuinely should not take long: connecting, the TLS handshake, and waiting
// for response headers.
func newHTTPClient(headers map[string]string) *http.Client {
	base := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          8,
		MaxIdleConnsPerHost:   4,
		MaxConnsPerHost:       8,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	if len(headers) == 0 {
		return &http.Client{Transport: base}
	}
	return &http.Client{Transport: &headerTransport{headers: headers, next: base}}
}

// headerTransport adds the configured headers to every request.
//
// The values come from resolved settings, never from the document's own
// `value` for a secret, so a credential lives encrypted in the settings store
// and is assembled here at the last moment.
type headerTransport struct {
	headers map[string]string
	next    http.RoundTripper
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Cloned: RoundTrip must not modify the request it is given, and the SDK
	// reuses request objects across retries.
	clone := req.Clone(req.Context())
	for name, value := range t.headers {
		clone.Header.Set(name, value)
	}
	return t.next.RoundTrip(clone)
}

// budget bounds calls to one server across every tool it offers.
//
// Nil means unbounded, so a server an operator has said to leave alone costs
// nothing at call time.
type budget struct {
	limiter *rate.Limiter
}

func newBudget(perSecond int) budget {
	if perSecond <= 0 {
		return budget{}
	}
	// Burst of one, for the same reason the host's per-tool limiter uses one:
	// a burst allowance is exactly what a model retrying in a loop consumes.
	return budget{limiter: rate.NewLimiter(rate.Limit(perSecond), 1)}
}

func (b budget) wait(ctx context.Context) error {
	if b.limiter == nil {
		return nil
	}
	if err := b.limiter.Wait(ctx); err != nil {
		return fmt.Errorf("this server is rate limited and the call did not get a "+
			"turn in time: %w", err)
	}
	return nil
}
