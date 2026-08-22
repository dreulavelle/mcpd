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
	// closed stops a dial after Shutdown, so a call arriving during teardown
	// cannot open a session nothing will close.
	closed bool
}

func newConn(endpoint string, headers map[string]string, impl *mcp.Implementation) *conn {
	return &conn{endpoint: endpoint, client: newHTTPClient(headers), impl: impl}
}

// session returns a live session, dialling if there is not one.
//
// The dial runs on a context detached from the caller's cancellation. The
// session outlives the call that happened to open it, and a caller that gives
// up mid-handshake would otherwise leave the next one to start again from
// nothing -- and, worse, could cancel a handshake several other callers were
// already waiting on.
func (c *conn) session(ctx context.Context) (*mcp.ClientSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errors.New("this server is shutting down")
	}
	if c.current != nil {
		return c.current, nil
	}

	dialCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dialTimeout)
	defer cancel()

	client := mcp.NewClient(c.impl, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint:   c.endpoint,
		HTTPClient: c.client,
		// This host does not act on server-initiated messages, and the
		// standalone stream is a connection held open per server for the life
		// of the process to receive them. Discovery is explicit here -- an
		// administrator asks for it, and classifies what comes back -- so a
		// tools/list_changed notification would have nowhere useful to go.
		DisableStandaloneSSE: true,
	}

	session, err := client.Connect(dialCtx, transport, nil)
	if err != nil {
		return nil, err
	}
	c.current = session
	return session, nil
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
func (c *conn) close() error {
	c.mu.Lock()
	session := c.current
	c.current = nil
	c.closed = true
	c.mu.Unlock()
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
