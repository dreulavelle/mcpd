package external

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePlugin is the far end of the pipe.
//
// Driving a Process over pipes rather than a compiled binary is what makes the
// interesting cases testable at all: a plugin that answers out of order, one
// that answers a request nobody is waiting for any more, and one that never
// answers. Each would otherwise need its own executable.
type fakePlugin struct {
	writeMu sync.Mutex
	out     *io.PipeWriter
}

// startFake wires a Process to a plugin that answers with handle.
//
// handle runs in its own goroutine per request, so the fake is a plugin that
// genuinely works on several calls at once. Returning nil answers nothing,
// which is how a hung call is modelled.
func startFake(t *testing.T, handle func(*fakePlugin, Request) *Response) (*Process, *fakePlugin) {
	t.Helper()

	pluginReads, toPlugin := io.Pipe()
	fromPlugin, pluginWrites := io.Pipe()

	f := &fakePlugin{out: pluginWrites}
	p := newProcess("fake", toPlugin, fromPlugin, quietLog())
	go p.readLoop()

	go func() {
		scanner := bufio.NewScanner(pluginReads)
		scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for scanner.Scan() {
			var req Request
			if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
				continue
			}
			go func() {
				if resp := handle(f, req); resp != nil {
					f.reply(resp)
				}
			}()
		}
	}()

	// Deliberately not waiting for handlers still in flight. Several tests
	// model a plugin that never answers, and waiting for one of those to
	// finish is waiting forever. Writing to a closed pipe is an error the
	// fake already ignores.
	t.Cleanup(func() {
		_ = pluginWrites.Close()
		_ = toPlugin.Close()
	})
	return p, f
}

// reply writes one frame back, serialised the way a real plugin's stdout is.
func (f *fakePlugin) reply(resp *Response) {
	line, err := json.Marshal(resp)
	if err != nil {
		return
	}
	f.writeMu.Lock()
	defer f.writeMu.Unlock()
	_, _ = f.out.Write(append(line, '\n'))
}

// closeOutput models a plugin whose stdout goes away mid-conversation.
func (f *fakePlugin) closeOutput() {
	f.writeMu.Lock()
	defer f.writeMu.Unlock()
	_ = f.out.Close()
}

func okResponse(id uint64, v any) *Response {
	raw, _ := json.Marshal(v)
	return &Response{ID: id, Result: raw}
}

// The bug this replaces: writeMu was taken before the round trip and held for
// its whole duration, so every caller queued behind the one in flight. Eight
// calls against a plugin that takes 100ms each took eight hundred
// milliseconds, and seven callers spent their deadlines waiting for a lock.
func TestProcess_ConcurrentCallsAreNotSerialised(t *testing.T) {
	const (
		callers = 8
		work    = 100 * time.Millisecond
	)
	p, _ := startFake(t, func(_ *fakePlugin, req Request) *Response {
		time.Sleep(work)
		return okResponse(req.ID, map[string]uint64{"id": req.ID})
	})

	start := time.Now()
	var wg sync.WaitGroup
	errs := make([]error, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var out map[string]uint64
			errs[i] = p.Call(context.Background(), "call_tool", nil, &out)
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
	}
	// Serialised would be callers*work. Half of that is a wide margin on a
	// loaded machine and still fails loudly on a regression.
	if limit := callers * work / 2; elapsed > limit {
		t.Errorf("%d concurrent calls took %s; serialised behaviour is back (limit %s)",
			callers, elapsed, limit)
	}
}

// A plugin may answer in whatever order it finishes. Matching by id is what
// makes that safe, and it is what the id was always for.
func TestProcess_ResponsesAreMatchedByID(t *testing.T) {
	p, _ := startFake(t, func(_ *fakePlugin, req Request) *Response {
		var in struct {
			Delay int `json:"delay_ms"`
		}
		_ = json.Unmarshal(req.Params, &in)
		time.Sleep(time.Duration(in.Delay) * time.Millisecond)
		return okResponse(req.ID, map[string]uint64{"id": req.ID})
	})

	const callers = 5
	got := make([]uint64, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Later callers answer sooner, so the stream comes back out of
			// the order it went out in.
			delay := (callers - i) * 20
			var out map[string]uint64
			errs[i] = p.Call(context.Background(), "call_tool",
				map[string]int{"delay_ms": delay}, &out)
			got[i] = out["id"]
		}()
	}
	wg.Wait()

	seen := map[uint64]bool{}
	for i, id := range got {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if id == 0 {
			t.Fatalf("caller %d got no response id", i)
		}
		if seen[id] {
			t.Fatalf("caller %d was handed response %d, which another caller already had", i, id)
		}
		seen[id] = true
	}
}

// The failure this exists for: a slow plugin made queued callers burn their
// deadlines, the first to acquire the lock hit ctx.Done and called Kill, and
// the plugin died for everyone -- including the callers that would have
// succeeded.
func TestProcess_ATimedOutCallDoesNotEndTheProcessForOthers(t *testing.T) {
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })

	p, _ := startFake(t, func(_ *fakePlugin, req Request) *Response {
		var in struct {
			Hang bool `json:"hang"`
		}
		_ = json.Unmarshal(req.Params, &in)
		if in.Hang {
			<-hang
			return nil
		}
		return okResponse(req.ID, map[string]string{"answer": "fine"})
	})

	slow := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		slow <- p.Call(ctx, "call_tool", map[string]bool{"hang": true}, nil)
	}()

	if err := <-slow; err == nil {
		t.Fatal("a call whose deadline passed must fail")
	}

	// The point of the whole change: the next caller is unaffected.
	var out map[string]string
	if err := p.Call(context.Background(), "call_tool", map[string]bool{"hang": false}, &out); err != nil {
		t.Fatalf("a call made after a timed-out one must still work: %v", err)
	}
	if out["answer"] != "fine" {
		t.Fatalf("out = %+v", out)
	}
	// The read loop is still up. Killing the process closes its stdout, which
	// ends the read loop, so this is what "the plugin was not terminated"
	// looks like from inside the host.
	select {
	case <-p.readDone:
		t.Error("one call timing out tore the plugin's stream down")
	default:
	}
	if !p.Running() {
		t.Error("one call timing out must not terminate the plugin")
	}
}

// A caller that gives up must leave nothing behind: no claim on its id, no
// goroutine, and no frame the next caller could mistake for its own.
func TestProcess_AnAbandonedCallIsCleanedUp(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	p, _ := startFake(t, func(_ *fakePlugin, req Request) *Response {
		var in struct {
			Slow bool `json:"slow"`
		}
		_ = json.Unmarshal(req.Params, &in)
		if in.Slow {
			<-release
		}
		return okResponse(req.ID, map[string]uint64{"id": req.ID})
	})

	before := runtime.NumGoroutine()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := p.Call(ctx, "call_tool", map[string]bool{"slow": true}, nil); err == nil {
		t.Fatal("the abandoned call must fail")
	}

	p.pendingMu.Lock()
	waiting := len(p.pending)
	p.pendingMu.Unlock()
	if waiting != 0 {
		t.Errorf("%d claims left behind by an abandoned call", waiting)
	}

	// Let the abandoned call's answer arrive. Whenever it lands, it must be
	// discarded rather than handed to whoever calls next.
	releaseOnce.Do(func() { close(release) })

	var out map[string]uint64
	if err := p.Call(context.Background(), "call_tool", map[string]bool{"slow": false}, &out); err != nil {
		t.Fatalf("the next call: %v", err)
	}
	if out["id"] != 2 {
		t.Errorf("the next caller was handed response %d, which belonged to the abandoned call",
			out["id"])
	}

	// The old shape spawned a goroutine per call and leaked it whenever the
	// call was abandoned; this one spawns none.
	for range 100 {
		if runtime.NumGoroutine() <= before+2 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("goroutines went from %d to %d; an abandoned call leaked one",
		before, runtime.NumGoroutine())
}

// A plugin that closes its output must wake every caller at once, rather than
// leaving each of them to find out by timing out separately.
func TestProcess_ClosedOutputWakesEveryWaiter(t *testing.T) {
	var once sync.Once
	p, _ := startFake(t, func(f *fakePlugin, _ Request) *Response {
		once.Do(f.closeOutput)
		return nil
	})

	done := make(chan error, 3)
	for range cap(done) {
		go func() { done <- p.Call(context.Background(), "call_tool", nil, nil) }()
	}

	deadline := time.After(5 * time.Second)
	for range cap(done) {
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("a call must fail once the plugin closes its output")
			}
			if !strings.Contains(err.Error(), "fake") {
				t.Errorf("the error should name the plugin: %v", err)
			}
		case <-deadline:
			t.Fatal("a caller was left waiting after the plugin closed its output")
		}
	}

	// And a call made afterwards is refused immediately rather than registered
	// against a read loop that has stopped.
	if err := p.Call(context.Background(), "call_tool", nil, nil); err == nil {
		t.Fatal("a call after the read loop stopped must be refused")
	}
}

// Shutdown still drains: the shutdown request goes out and is answered before
// the input is closed.
func TestProcess_StopDrainsCleanly(t *testing.T) {
	var mu sync.Mutex
	var sawShutdown bool
	p, _ := startFake(t, func(_ *fakePlugin, req Request) *Response {
		if req.Method == MethodShutdown {
			mu.Lock()
			sawShutdown = true
			mu.Unlock()
		}
		return okResponse(req.ID, nil)
	})

	if err := p.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !sawShutdown {
		t.Error("stop must ask the plugin to exit before closing its input")
	}
}

// A plugin error still reaches its own caller and nobody else's.
func TestProcess_AnErrorReachesOnlyItsCaller(t *testing.T) {
	p, _ := startFake(t, func(_ *fakePlugin, req Request) *Response {
		var in struct {
			Fail bool `json:"fail"`
		}
		_ = json.Unmarshal(req.Params, &in)
		if in.Fail {
			return &Response{ID: req.ID, Error: &Error{Code: CodeInternal, Message: "boom"}}
		}
		return okResponse(req.ID, map[string]string{"ok": "yes"})
	})

	var bad, good error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		bad = p.Call(context.Background(), "call_tool", map[string]bool{"fail": true}, nil)
	}()
	go func() {
		defer wg.Done()
		var out map[string]string
		good = p.Call(context.Background(), "call_tool", map[string]bool{"fail": false}, &out)
	}()
	wg.Wait()

	var perr *Error
	if !errors.As(bad, &perr) || perr.Code != CodeInternal {
		t.Errorf("the failing caller got %v, want the plugin's own error", bad)
	}
	if good != nil {
		t.Errorf("the other caller was affected: %v", good)
	}
}

// BenchmarkProcessCall compares multiplexing against the shape it replaced: one
// lock held across the whole round trip. Both run against the same fake plugin,
// so the difference measured is the host's own behaviour and nothing else.
func BenchmarkProcessCall(b *testing.B) {
	const work = 2 * time.Millisecond

	newFake := func(b *testing.B) *Process {
		pluginReads, toPlugin := io.Pipe()
		fromPlugin, pluginWrites := io.Pipe()
		p := newProcess("bench", toPlugin, fromPlugin, quietLog())
		go p.readLoop()

		var writeMu sync.Mutex
		go func() {
			scanner := bufio.NewScanner(pluginReads)
			scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
			for scanner.Scan() {
				var req Request
				if json.Unmarshal(scanner.Bytes(), &req) != nil {
					continue
				}
				go func() {
					time.Sleep(work)
					line, _ := json.Marshal(&Response{ID: req.ID, Result: json.RawMessage(`{}`)})
					writeMu.Lock()
					_, _ = pluginWrites.Write(append(line, '\n'))
					writeMu.Unlock()
				}()
			}
		}()
		b.Cleanup(func() {
			_ = pluginWrites.Close()
			_ = toPlugin.Close()
		})
		return p
	}

	b.Run("multiplexed", func(b *testing.B) {
		p := newFake(b)
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				if err := p.Call(context.Background(), "call_tool", nil, nil); err != nil {
					b.Fatal(err)
				}
			}
		})
	})

	b.Run("serialised", func(b *testing.B) {
		p := newFake(b)
		var roundTrip sync.Mutex
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				// The shape this replaced: one lock held for the whole round
				// trip, so no two callers are ever in flight together.
				roundTrip.Lock()
				err := p.Call(context.Background(), "call_tool", nil, nil)
				roundTrip.Unlock()
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	})
}
