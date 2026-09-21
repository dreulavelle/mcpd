package tunnel

import (
	"context"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openai/tunnel-client/pkg/tunnelctx"
)

// commandSessions is the transport handed to the tunnel client: every command
// it forwards gets an in-memory pipe and an MCP server session of its own.
//
// The client does not treat an injected transport that way. It connects once
// and shares that one connection between all of its workers -- up to ten
// commands at a time -- each writing its request and then reading whatever
// message comes off the pipe next. ChatGPT numbers every request 0, so a
// worker cannot tell its own answer from a neighbour's: two tool calls in
// flight together could each be handed the other's result, and the worker
// left without one waits out the command deadline. At the deadline the client
// closes the shared connection and connects again, and an in-memory pair
// cannot be reconnected -- the new connection is the old, closed pipe. From
// then on every call on that tunnel failed with a closed pipe until the tunnel
// was rebuilt, whatever it asked and of whichever plugin. That was the
// "intermittent" 3CX failure: one slow or crossed call, then nothing.
//
// The client's own in-process channel avoids both by giving each request a
// fresh pipe and session and serialising requests over it; its injected
// transport has neither. So the separation happens here: the command's id is
// in the context of every read and write the worker makes, and that id picks
// the session. A request cannot be answered with another's result because no
// other request is on its pipe, and a command that times out closes only its
// own session -- which also cancels its tool handler, since the SDK cancels
// in-flight requests when their connection's reader stops.
//
// This relies on ChatGPT's requests being self-contained: each carries its
// protocol version and capabilities rather than depending on an earlier
// initialize on the same session. A client that did a separate handshake would
// find each request on a session that had not seen it.
type commandSessions struct {
	runCtx  context.Context
	server  *mcp.Server
	observe func()

	// done closes when the run context has ended and every session with it.
	done chan struct{}

	mu      sync.Mutex
	open    map[string]*commandSession
	stopped bool
	running sync.WaitGroup
}

type commandSession struct {
	conn   mcp.Connection
	opened time.Time
}

// staleSession is how long a session may stay open with nobody having read its
// answer. The client gives up on a command well before its connection TTL, so
// a session this old belongs to a worker that left without saying so.
const staleSession = connectionMaxTTL + time.Minute

func newCommandSessions(runCtx context.Context, server *mcp.Server, observe func()) *commandSessions {
	s := &commandSessions{
		runCtx:  runCtx,
		server:  server,
		observe: observe,
		done:    make(chan struct{}),
		open:    make(map[string]*commandSession),
	}
	go func() {
		<-runCtx.Done()
		s.mu.Lock()
		s.stopped = true
		open := s.open
		s.open = nil
		s.mu.Unlock()
		for _, cs := range open {
			_ = cs.conn.Close()
		}
		s.running.Wait()
		close(s.done)
	}()
	return s
}

// Connect is called by the client once, and again whenever it has closed the
// connection it shares. Every call returns the same router, because closing it
// is not an event any one session should hear about.
func (s *commandSessions) Connect(context.Context) (mcp.Connection, error) {
	if err := s.runCtx.Err(); err != nil {
		return nil, err
	}
	return commandRouter{s}, nil
}

// commandKey names the command a read or write belongs to. The client puts
// its request id in the context before it touches the transport; a caller
// without one shares the unnamed session, which is how every call behaved
// before.
func commandKey(ctx context.Context) string {
	id, _ := tunnelctx.RequestIDFromContext(ctx)
	return id
}

func (s *commandSessions) sessionFor(key string) (*commandSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return nil, mcp.ErrConnectionClosed
	}
	if cs := s.open[key]; cs != nil {
		return cs, nil
	}
	s.sweepLocked(time.Now())

	serverSide, tunnelSide := mcp.NewInMemoryTransports()
	var transport mcp.Transport = serverSide
	if s.observe != nil {
		transport = observedTransport{inner: serverSide, observe: s.observe}
	}
	session, err := s.server.Connect(s.runCtx, transport, nil)
	if err != nil {
		return nil, err
	}
	conn, err := tunnelSide.Connect(s.runCtx)
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	s.running.Add(1)
	go func() {
		defer s.running.Done()
		_ = session.Wait()
	}()
	cs := &commandSession{conn: conn, opened: time.Now()}
	s.open[key] = cs
	return cs, nil
}

func (s *commandSessions) lookup(key string) *commandSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.open[key]
}

// end closes a command's session. Closing the pipe stops the server
// session's reader, and the SDK cancels whatever handler is still running on
// it: a tool call nobody is waiting for any more stops rather than finishing
// for no one.
func (s *commandSessions) end(key string, cs *commandSession) {
	s.mu.Lock()
	if s.open[key] == cs {
		delete(s.open, key)
	}
	s.mu.Unlock()
	_ = cs.conn.Close()
}

// sweepLocked closes sessions whose worker left without reading an answer or
// failing, so a path that forgets to finish cannot hold a session for the
// life of the tunnel.
func (s *commandSessions) sweepLocked(now time.Time) {
	for key, cs := range s.open {
		if now.Sub(cs.opened) > staleSession {
			delete(s.open, key)
			_ = cs.conn.Close()
		}
	}
}

// commandRouter is the connection the client shares between its workers. It
// holds nothing of its own: each read and write goes to the session of the
// command named in its context.
type commandRouter struct{ s *commandSessions }

func (r commandRouter) Write(ctx context.Context, msg jsonrpc.Message) error {
	key := commandKey(ctx)
	cs, err := r.s.sessionFor(key)
	if err != nil {
		return err
	}
	if err := cs.conn.Write(ctx, msg); err != nil {
		r.s.end(key, cs)
		return err
	}
	// A notification gets no answer, so nothing will read from this session
	// again. The in-memory pipe does not return from a write until the server
	// has taken the message, so closing now loses nothing.
	if req, ok := msg.(*jsonrpc.Request); ok && !req.ID.IsValid() {
		r.s.end(key, cs)
	}
	return nil
}

func (r commandRouter) Read(ctx context.Context) (jsonrpc.Message, error) {
	key := commandKey(ctx)
	cs := r.s.lookup(key)
	if cs == nil {
		return nil, mcp.ErrConnectionClosed
	}
	msg, err := cs.conn.Read(ctx)
	if err != nil {
		// Whether the command ran out of time or the session failed, this
		// command is finished with it; the next one gets a fresh session.
		r.s.end(key, cs)
		return nil, err
	}
	// A response is the end of a command. Anything else -- a progress or log
	// notification -- is part of it, and the worker reads on.
	if _, ok := msg.(*jsonrpc.Response); ok {
		r.s.end(key, cs)
	}
	return msg, nil
}

// Close is how the client lets go of the shared connection when one command
// fails, and it has no way to say which. Closing every session here would
// fail every command in flight with it -- the collateral this type exists to
// prevent -- so it closes nothing: the failing command's own read or write
// has already ended its session, and the sweep catches one that did not.
func (commandRouter) Close() error { return nil }

func (commandRouter) SessionID() string { return "" }

var _ mcp.Connection = commandRouter{}
