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
type Process struct {
	name string
	log  *slog.Logger

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader

	// writeMu serialises requests. The protocol is one frame at a time in each
	// direction, so a plugin never has to multiplex.
	writeMu sync.Mutex
	nextID  atomic.Uint64

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

	p := &Process{
		name:   m.Name,
		log:    log,
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewReaderSize(stdout, 64<<10),
		exited: make(chan struct{}),
	}

	// A plugin's stderr becomes host log lines rather than being discarded,
	// which is often the only way to diagnose one that will not start.
	go p.drainStderr(stderr)
	go p.reap()

	return p, nil
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

// Call sends a request and waits for its response.
//
// The protocol is strictly request/response with one frame in flight, so the
// write lock is held across the round trip. That serialises a plugin's work,
// which is the right default: a plugin author should not have to make their
// integration concurrency-safe to be usable.
func (p *Process) Call(ctx context.Context, method string, params any, result any) error {
	select {
	case <-p.exited:
		return fmt.Errorf("external: plugin %s is not running", p.name)
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

	p.writeMu.Lock()
	defer p.writeMu.Unlock()

	done := make(chan error, 1)
	go func() { done <- p.roundTrip(req, result) }()

	select {
	case err := <-done:
		return err
	case <-p.exited:
		return fmt.Errorf("external: plugin %s exited during %s: %w", p.name, method, p.exitErr)
	case <-ctx.Done():
		// A plugin that stops answering cannot be left holding the pipe: the
		// next call would block behind it forever. Killing it is the only way
		// to recover the channel.
		p.log.Error("plugin call timed out; terminating the process",
			"plugin", p.name, "method", method)
		p.Kill()
		return fmt.Errorf("external: plugin %s did not answer %s within %s",
			p.name, method, timeoutFor(method))
	}
}

func (p *Process) roundTrip(req Request, result any) error {
	line, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if _, err := p.stdin.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("external: write to plugin %s: %w", p.name, err)
	}

	for {
		frame, err := p.readFrame()
		if err != nil {
			return err
		}

		var resp Response
		if err := json.Unmarshal(frame, &resp); err != nil {
			return fmt.Errorf("external: plugin %s sent an unreadable frame: %w", p.name, err)
		}
		// A stale response from a previous timed-out call must not be mistaken
		// for this one's.
		if resp.ID != req.ID {
			p.log.Warn("discarding a plugin response with an unexpected id",
				"plugin", p.name, "want", req.ID, "got", resp.ID)
			continue
		}
		if resp.Error != nil {
			return resp.Error
		}
		if result == nil || len(resp.Result) == 0 {
			return nil
		}
		return json.Unmarshal(resp.Result, result)
	}
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
	var err error
	p.stopOnce.Do(func() {
		shutdownCtx, cancel := context.WithTimeout(ctx, shutdownCallTimeout)
		defer cancel()

		// Best effort: a plugin that is already wedged will not answer, and
		// that is what the kill below is for.
		_ = p.Call(shutdownCtx, MethodShutdown, nil, nil)
		_ = p.stdin.Close()

		select {
		case <-p.exited:
		case <-time.After(shutdownCallTimeout):
			p.log.Warn("plugin did not exit after shutdown; killing", "plugin", p.name)
			p.Kill()
			<-p.exited
		}
	})
	return err
}

// Kill terminates the subprocess immediately.
func (p *Process) Kill() {
	if p.cmd.Process != nil {
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
