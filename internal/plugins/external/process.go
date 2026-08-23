package external

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// maxFrame bounds a single JSON-RPC frame. A plugin returning an unbounded
// response would otherwise exhaust host memory, and a plugin is exactly the
// component least likely to be trustworthy about its own output size.
const maxFrame = 8 << 20

// callTimeout bounds a single request. A plugin that hangs must not pin the
// caller indefinitely; mutations get a longer budget than reads because an
// upstream write can legitimately take a while.
const (
	defaultCallTimeout  = 30 * time.Second
	mutationCallTimeout = 2 * time.Minute
	describeCallTimeout = 15 * time.Second
	shutdownCallTimeout = 5 * time.Second
)

// Process is a running plugin subprocess.
//
// Requests are multiplexed over the pipe rather than serialised behind one
// round trip. The write lock covers the write and nothing else; a response is
// matched back to its caller by id, which is what the id was always for. What
// that buys is not throughput so much as independence: one slow call no longer
// spends every other caller's deadline, and one caller giving up no longer
// ends the process for everybody else.
type Process struct {
	name string
	log  *slog.Logger

	// cmd is nil for a Process driven over pipes by a test. Kill and Stop
	// both cope, so the subprocess is the only optional part of this.
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader

	// writeSlot is a mutex that can be given up on. A plugin that has stopped
	// reading its stdin will eventually block a writer, and a sync.Mutex would
	// then queue every other caller behind it with no way out -- which is the
	// shape this file exists to remove. Acquiring it selects on the caller's
	// context, so a caller that cannot get the pipe fails on its own deadline
	// rather than on somebody else's work.
	writeSlot chan struct{}
	nextID    atomic.Uint64

	// pending maps an in-flight request id to the channel its response will
	// arrive on. A nil map means the read loop has stopped.
	pendingMu sync.Mutex
	pending   map[uint64]chan *Response

	// readDone closes when the read loop stops, for any reason; readErr says
	// why. A caller blocked on a response is woken by it, so a plugin that
	// closes its output does not leave every caller to time out separately.
	readDone chan struct{}
	readErr  error

	// exited is closed when the subprocess terminates, so a call in flight
	// fails immediately rather than blocking until its timeout.
	exited   chan struct{}
	exitErr  error
	stopOnce sync.Once
}

// Spawn starts a plugin subprocess.
func Spawn(ctx context.Context, dir string, m Manifest, env map[string]string, log *slog.Logger) (*Process, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}

	execPath := filepath.Join(dir, m.Exec)
	// Resolve and re-check containment: a symlink inside the directory could
	// otherwise point anywhere.
	resolved, err := filepath.EvalSymlinks(execPath)
	if err != nil {
		return nil, fmt.Errorf("external: plugin %s executable %s: %w", m.Name, m.Exec, err)
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, fmt.Errorf("external: plugin %s directory: %w", m.Name, err)
	}
	rel, err := filepath.Rel(realDir, resolved)
	if err != nil || rel == ".." || len(rel) > 2 && rel[:3] == "../" {
		return nil, fmt.Errorf(
			"external: plugin %s executable resolves outside its own directory", m.Name)
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return nil, err
	}
	if info.Mode()&0o111 == 0 {
		return nil, fmt.Errorf("external: plugin %s executable %s is not executable", m.Name, m.Exec)
	}

	cmd := exec.CommandContext(ctx, resolved, m.Args...)
	cmd.Dir = dir
	// A deliberately minimal environment. Inheriting the host's would hand
	// every plugin every secret mcpd was started with.
	cmd.Env = buildEnv(env)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("external: start plugin %s: %w", m.Name, err)
	}

	p := newProcess(m.Name, stdin, stdout, log)
	p.cmd = cmd

	// A plugin's stderr becomes host log lines rather than being discarded,
	// which is often the only way to diagnose one that will not start.
	go p.drainStderr(stderr)
	go p.reap()
	go p.readLoop()

	return p, nil
}

// newProcess builds the half of a Process that needs only a pair of pipes.
//
// Spawn adds the subprocess; a test supplies its own pipes and drives the far
// end itself, which is the only way to exercise multiplexing, a plugin that
// answers out of order, and a plugin that never answers at all without
// compiling a binary for each case.
func newProcess(name string, stdin io.WriteCloser, stdout io.Reader, log *slog.Logger) *Process {
	if log == nil {
		log = slog.Default()
	}
	return &Process{
		name:      name,
		log:       log,
		stdin:     stdin,
		stdout:    bufio.NewReaderSize(stdout, 64<<10),
		writeSlot: make(chan struct{}, 1),
		pending:   make(map[uint64]chan *Response),
		readDone:  make(chan struct{}),
		exited:    make(chan struct{}),
	}
}

// buildEnv assembles the subprocess environment.
func buildEnv(env map[string]string) []string {
	// PATH and HOME are the minimum a well-behaved program expects; TZ keeps
	// timestamp formatting consistent with the host.
	out := []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=/tmp",
	}
	if tz, ok := os.LookupEnv("TZ"); ok {
		out = append(out, "TZ="+tz)
	}
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

func (p *Process) drainStderr(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		p.log.Warn("plugin stderr", "plugin", p.name, "line", scanner.Text())
	}
}

func (p *Process) reap() {
	err := p.cmd.Wait()
	p.exitErr = err
	close(p.exited)
	if err != nil {
		p.log.Error("plugin exited", "plugin", p.name, "error", err)
	} else {
		p.log.Info("plugin exited cleanly", "plugin", p.name)
	}
}

// readLoop is the only reader of the plugin's stdout.
//
// One goroutine owns the pipe for the process's whole life, which is what
// makes concurrent calls safe: nothing else advances the stream, so no caller
// can consume another's frame or leave the reader stopped mid-line.
func (p *Process) readLoop() {
	defer p.stopReading()

	for {
		frame, err := p.readFrame()
		if err != nil {
			p.readErr = err
			return
		}

		var resp Response
		if err := json.Unmarshal(frame, &resp); err != nil {
			// One unreadable frame is not a reason to abandon the stream: a
			// whole line was consumed, so the reader is still aligned, and
			// whoever was waiting on that id will time out and say so.
			p.log.Warn("discarding an unreadable frame from a plugin",
				"plugin", p.name, "error", err)
			continue
		}
		p.deliver(&resp)
	}
}

// stopReading wakes every caller still waiting. Without it, a plugin that
// closed its output would leave each of them to discover that by timeout.
func (p *Process) stopReading() {
	p.pendingMu.Lock()
	waiters := p.pending
	// Nil rather than empty: a call arriving after this point must be refused
	// outright rather than registered on a map nothing will ever read.
	p.pending = nil
	p.pendingMu.Unlock()

	close(p.readDone)
	for _, ch := range waiters {
		close(ch)
	}
}

// deliver hands a response to whoever is waiting for it.
func (p *Process) deliver(resp *Response) {
	p.pendingMu.Lock()
	ch, waiting := p.pending[resp.ID]
	if waiting {
		delete(p.pending, resp.ID)
	}
	p.pendingMu.Unlock()

	if !waiting {
		// A response to a call that has already given up. Discarding it by id
		// is the point: before this, the next caller read it as their own.
		p.log.Warn("discarding a plugin response nobody is waiting for",
			"plugin", p.name, "id", resp.ID)
		return
	}
	// Buffered by one and written only here, so this cannot block even if the
	// caller stopped selecting on it a moment ago.
	ch <- resp
}

// Call sends a request and waits for its response.
//
// Waiting happens off the write lock, so a plugin taking two minutes over one
// mutation does not spend the deadline of every read queued behind it -- which
// is what used to make a single slow call look like a plugin-wide failure.
func (p *Process) Call(ctx context.Context, method string, params any, result any) error {
	select {
	case <-p.exited:
		return fmt.Errorf("external: plugin %s is not running", p.name)
	case <-p.readDone:
		return fmt.Errorf("external: plugin %s is not answering: %w", p.name, p.readErr)
	default:
	}

	ctx, cancel := context.WithTimeout(ctx, timeoutFor(method))
	defer cancel()

	var raw json.RawMessage
	if params != nil {
		encoded, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("external: encode %s params: %w", method, err)
		}
		raw = encoded
	}

	req := Request{ID: p.nextID.Add(1), Method: method, Params: raw}

	// Registered before the write. A plugin fast enough to answer between the
	// two would otherwise have its response discarded as unclaimed, and the
	// caller would wait out a whole timeout for a reply that already arrived.
	replies, err := p.register(req.ID)
	if err != nil {
		return err
	}
	defer p.forget(req.ID)

	if err := p.write(ctx, req); err != nil {
		return err
	}

	select {
	case resp, ok := <-replies:
		if !ok {
			// The read loop stopped. Whatever it hit is the real failure.
			return fmt.Errorf("external: plugin %s stopped answering during %s: %w",
				p.name, method, p.readErr)
		}
		return decodeResponse(resp, result)
	case <-p.exited:
		return fmt.Errorf("external: plugin %s exited during %s: %w", p.name, method, p.exitErr)
	case <-ctx.Done():
		// Only this call fails. The process is deliberately left alone: it was
		// killed here once, which took down every other caller -- including
		// the ones about to succeed -- to recover a pipe that was not blocked.
		// A late answer is discarded by id, and a plugin that has genuinely
		// stopped answering fails every call and reports itself unhealthy.
		p.log.Warn("plugin call timed out", "plugin", p.name, "method", method,
			"timeout", timeoutFor(method))
		return fmt.Errorf("external: plugin %s did not answer %s within %s",
			p.name, method, timeoutFor(method))
	}
}

// register claims an id and the channel its answer will arrive on.
func (p *Process) register(id uint64) (chan *Response, error) {
	ch := make(chan *Response, 1)
	p.pendingMu.Lock()
	defer p.pendingMu.Unlock()
	if p.pending == nil {
		return nil, fmt.Errorf("external: plugin %s is not answering: %w", p.name, p.readErr)
	}
	p.pending[id] = ch
	return ch, nil
}

// forget drops a claim. Delivery removes it too, so this is a no-op on the
// path where an answer arrived and a cleanup on every other.
func (p *Process) forget(id uint64) {
	p.pendingMu.Lock()
	delete(p.pending, id)
	p.pendingMu.Unlock()
}

// write puts one frame on the pipe, holding the slot for exactly that long.
func (p *Process) write(ctx context.Context, req Request) error {
	line, err := json.Marshal(req)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	select {
	case p.writeSlot <- struct{}{}:
	case <-ctx.Done():
		return fmt.Errorf("external: plugin %s did not free its input for %s within %s",
			p.name, req.Method, timeoutFor(req.Method))
	case <-p.exited:
		return fmt.Errorf("external: plugin %s exited before %s was sent: %w",
			p.name, req.Method, p.exitErr)
	case <-p.readDone:
		return fmt.Errorf("external: plugin %s is not answering: %w", p.name, p.readErr)
	}
	defer func() { <-p.writeSlot }()

	if _, err := p.stdin.Write(line); err != nil {
		return fmt.Errorf("external: write to plugin %s: %w", p.name, err)
	}
	return nil
}

// decodeResponse turns one answer into the caller's result.
func decodeResponse(resp *Response, result any) error {
	if resp.Error != nil {
		return resp.Error
	}
	if result == nil || len(resp.Result) == 0 {
		return nil
	}
	return json.Unmarshal(resp.Result, result)
}

// readFrame reads one line, refusing anything over the frame cap.
func (p *Process) readFrame() ([]byte, error) {
	var buf []byte
	for {
		chunk, isPrefix, err := p.stdout.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("external: plugin %s closed its output", p.name)
			}
			return nil, fmt.Errorf("external: read from plugin %s: %w", p.name, err)
		}
		buf = append(buf, chunk...)
		if len(buf) > maxFrame {
			// The rest of the line has not been read, so the stream position is
			// no longer known and no later frame can be trusted. This is the
			// one case where the process still has to go.
			p.Kill()
			return nil, fmt.Errorf("external: plugin %s sent a frame over %d bytes", p.name, maxFrame)
		}
		if !isPrefix {
			return buf, nil
		}
	}
}

// timeoutFor gives mutations a longer budget than reads.
func timeoutFor(method string) time.Duration {
	switch method {
	case MethodApply, MethodPlan, MethodObserve:
		return mutationCallTimeout
	case MethodDescribe:
		return describeCallTimeout
	case MethodShutdown:
		return shutdownCallTimeout
	default:
		return defaultCallTimeout
	}
}

// Stop asks the plugin to exit, then kills it if it does not.
func (p *Process) Stop(ctx context.Context) error {
	p.stopOnce.Do(func() {
		shutdownCtx, cancel := context.WithTimeout(ctx, shutdownCallTimeout)
		defer cancel()

		// Best effort: a plugin that is already wedged will not answer, and
		// that is what the kill below is for.
		_ = p.Call(shutdownCtx, MethodShutdown, nil, nil)
		_ = p.stdin.Close()

		if p.cmd == nil {
			// Driven over pipes rather than by a subprocess. Closing the input
			// is the whole of the shutdown; there is nothing to wait for.
			return
		}
		select {
		case <-p.exited:
		case <-time.After(shutdownCallTimeout):
			p.log.Warn("plugin did not exit after shutdown; killing", "plugin", p.name)
			p.Kill()
			<-p.exited
		}
	})
	return nil
}

// Kill terminates the subprocess immediately.
func (p *Process) Kill() {
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

// Running reports whether the subprocess is still alive.
func (p *Process) Running() bool {
	select {
	case <-p.exited:
		return false
	default:
		return true
	}
}
