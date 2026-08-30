package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"syscall"
	"time"

	"github.com/spoked/mcpd/internal/auth/users"
	"github.com/spoked/mcpd/internal/settings"
)

// isPermissionDenied reports whether an error is an EACCES from bind.
func isPermissionDenied(err error) bool {
	return errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM)
}

// startWorker runs a long-lived component, logging an unexpected exit.
//
// Every background goroutine in mcpd is started here so that each one has an
// owner, a shutdown signal, and somewhere for its error to surface. A
// goroutine started anywhere else would have none of those.
func (a *App) startWorker(name string, ctx context.Context, run func(context.Context) error) {
	a.workers.Add(1)
	go func() {
		defer a.workers.Done()
		if err := run(ctx); err != nil && ctx.Err() == nil {
			a.log.ErrorContext(ctx, "background worker stopped unexpectedly", "worker", name, "error", err)
			return
		}
		a.log.DebugContext(ctx, "background worker stopped", "worker", name)
	}()
}

// waitForWorkers blocks until every worker has returned or the budget expires.
func (a *App) waitForWorkers(budget time.Duration) bool {
	done := make(chan struct{})
	go func() {
		a.workers.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(budget):
		return false
	}
}

// scanClaimable executes approved work left over from a previous run.
//
// It is a one-shot pass rather than a loop: the outbox publisher redelivers
// pending approval events, and the reaper catches anything stuck. This only
// covers the gap where an event was published and acknowledged but its
// consumer died before acting.
func (a *App) scanClaimable(ctx context.Context) error {
	pending, err := a.ops.Claimable(ctx, 100)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	a.log.InfoContext(ctx, "resuming approved operations left from a previous run", "count", len(pending))
	for _, op := range pending {
		if ctx.Err() != nil {
			return nil
		}
		if err := a.executor.Execute(ctx, op.ID); err != nil {
			a.log.ErrorContext(ctx, "failed to resume an operation", "operation_id", op.ID, "error", err)
		}
	}
	return nil
}

// Run starts every component and blocks until ctx is cancelled, then shuts
// down in reverse order.
func (a *App) Run(ctx context.Context) error {
	if err := a.manager.Start(ctx); err != nil {
		return err
	}

	// Background workers share a context cancelled at shutdown, and each is
	// tracked so Shutdown can wait for it rather than abandoning work
	// mid-transaction.
	workerCtx, stopWorkers := context.WithCancel(context.Background())
	a.stopWorkers = stopWorkers

	a.startWorker("outbox-publisher", workerCtx, a.publisher.Run)
	a.startWorker("reaper", workerCtx, a.reaper.Run)
	if a.accounts != nil {
		hk := users.NewHousekeeper(a.accounts, time.Hour)
		a.startWorker("session-housekeeper", workerCtx, hk.Run)
	}
	a.startWorker("sso-state-housekeeper", workerCtx, a.purgeSSOStates)

	a.startWorker("history-retention", workerCtx, a.pruneHistory)
	// Asks each imported remote server what it offers, on a timer, so a tool
	// that changed under an approval is caught without anybody looking.
	a.startWorker("mcp-rediscovery", workerCtx, a.rediscover)

	// Anything approved while the process was down still needs executing. The
	// event announcing it was consumed, or never delivered, so a startup scan
	// is what makes the executor restart-safe.
	a.startWorker("claimable-scan", workerCtx, a.scanClaimable)

	if a.settings.FieldBool(ctx, settings.KeyTunnelUpdates) {
		a.startWorker("tunnel-version-check", workerCtx, a.tunnelCheck.Run)
	}

	// The tunnel connects in the background: a control plane that is slow or
	// unreachable must not hold up the listeners, and it can be started from
	// the dashboard afterwards.
	//
	// It waits for the listeners first. A connected tunnel can carry a tool
	// call immediately, and answering one before the host is serving would
	// report a failure for a plugin that was moments from being ready.
	if a.tunnels.Enabled() {
		a.startWorker("tunnel", workerCtx, func(ctx context.Context) error {
			select {
			case <-a.serving:
			case <-ctx.Done():
				return nil
			}
			if err := a.tunnels.Start(ctx); err != nil {
				// Already recorded on the tunnel's status and logged there.
				// Returning nil keeps a tunnel failure from looking like a
				// crashed worker.
				return nil
			}
			<-ctx.Done()
			return a.tunnels.Stop(context.WithoutCancel(ctx))
		})
	}

	errCh := make(chan error, 2)

	if a.frontend != nil {
		go func() {
			a.log.InfoContext(ctx, "dashboard listening", "addr", a.frontend.Addr)
			if err := a.frontend.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				// A privileged port is the likeliest cause, and the message
				// has to say so: "permission denied" alone sends an operator
				// looking in the wrong place.
				if isPermissionDenied(err) {
					errCh <- fmt.Errorf(
						"app: dashboard cannot bind %s: binding a port below 1024 needs "+
							"CAP_NET_BIND_SERVICE. Either grant it (the systemd unit has "+
							"AmbientCapabilities set), publish a high port and map it, or "+
							"set server.frontend_listen to a port above 1024: %w",
						a.frontend.Addr, err)
					return
				}
				errCh <- fmt.Errorf("app: dashboard server: %w", err)
				return
			}
		}()
	}

	// Bound here rather than inside ListenAndServe, so a failure to bind is a
	// startup error rather than something reported from a goroutine.
	listener, err := net.Listen("tcp", a.cfg.Server.Listen)
	if err != nil {
		return fmt.Errorf("app: cannot bind %s: %w", a.cfg.Server.Listen, err)
	}
	// Binding is not serving. The socket accepts into the kernel backlog the
	// moment it is bound, so a client can connect and then wait forever for a
	// TLS handshake that nothing is running yet -- which arrives as EOF, and
	// looks nothing like "too early". Readiness is therefore a completed
	// request, not a bound port.
	go a.announceServing(workerCtx, listener.Addr())

	go func() {
		a.log.InfoContext(ctx, "http server listening",
			"addr", a.cfg.Server.Listen,
			"public_url", a.publicURL(workerCtx),
			"plugins", a.manager.Names())
		serve := func() error { return a.server.Serve(listener) }
		if a.server.TLSConfig != nil {
			// The certificate is already in TLSConfig, so the file arguments
			// are deliberately empty.
			serve = func() error { return a.server.ServeTLS(listener, "", "") }
		}
		if err := serve(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("app: http server: %w", err)
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		if err != nil {
			return err
		}
		return nil
	case reason := <-a.restartCh:
		// The same drain a signal takes. A restart is not a special kind of
		// stop: this process exits cleanly and something outside it starts
		// another, so the only thing that makes it a restart rather than a
		// shutdown is what is supervising -- which is why the dashboard is
		// told there is no restart when nothing is.
		a.log.InfoContext(ctx, "restarting", "reason", reason)
		return a.Shutdown()
	case <-ctx.Done():
		a.log.InfoContext(ctx, "shutdown signal received")
		return a.Shutdown()
	}
}

// RequestRestart asks Run to drain and exit, so that the supervisor starts a
// new process.
//
// Buffered by one and non-blocking: two operators pressing the button at the
// same moment should not have the second wait for a drain that is already
// under way, and a restart requested before Run reaches its select must not
// deadlock the request that asked for it.
func (a *App) RequestRestart(reason string) error {
	select {
	case a.restartCh <- reason:
		return nil
	default:
		return errors.New("a restart is already under way")
	}
}

// Shutdown drains the application in a deliberate order.
//
// The ordering matters more than the speed. Step one takes the host out of
// rotation so the proxy stops sending work; only then are in-flight requests
// drained. Plugins stop before the database closes, because a plugin's
// teardown may still need to write.
func (a *App) Shutdown() error {
	// Deliberately detached from the cancelled context that triggered
	// shutdown: every step below needs a live context to do its work.
	//
	// The budget is read here rather than at startup, which is what lets it be
	// a live setting: a change to it applies to the next stop rather than to
	// the start after it.
	ctx, cancel := context.WithTimeout(
		context.Background(), a.shutdownTimeout(context.Background()))
	defer cancel()

	var errs []error

	// 1. Stop accepting new requests and drain those in flight. The readiness
	//    probe already reports the server closing once ListenAndServe returns.
	if a.frontend != nil {
		if err := a.frontend.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("app: drain dashboard: %w", err))
		}
	}
	if err := a.server.Shutdown(ctx); err != nil {
		errs = append(errs, fmt.Errorf("app: drain http server: %w", err))
	}
	a.log.Info("http servers drained")

	// 2. Stop background workers and wait for them.
	//
	// The publisher performs a final drain on the way out, so events committed
	// moments before shutdown still reach consumers rather than waiting for
	// the next start. An in-flight execution is left to its own context: an
	// abandoned mutation would land in indeterminate on the next sweep, which
	// is correct but noisy, so the wait is generous enough to let it settle.
	if a.stopWorkers != nil {
		a.stopWorkers()
	}
	if !a.waitForWorkers(20 * time.Second) {
		a.log.Warn("background workers did not stop within the drain budget")
	} else {
		a.log.Info("background workers stopped")
	}

	if a.bus != nil {
		if err := a.bus.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	// The catalogue caches own the refreshes that run behind a served answer.
	// Nobody is waiting for one, so without this the process would be held
	// open on the way out by work whose result nothing will read.
	if a.catalog != nil {
		if err := a.catalog.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	// 3. Stop plugins, in reverse registration order.
	pluginCtx, pluginCancel := context.WithTimeout(ctx, 10*time.Second)
	if err := a.manager.Shutdown(pluginCtx); err != nil {
		errs = append(errs, err)
	}
	pluginCancel()
	a.log.Info("plugins stopped")

	// 4. Close the database last, checkpointing the WAL so the file is
	//    self-contained for backup on exit.
	if err := a.db.Close(); err != nil {
		errs = append(errs, fmt.Errorf("app: close database: %w", err))
	}
	a.log.Info("database closed")

	// 5. Let queued crash reports finish going out.
	//
	// After everything else and bounded tightly. The report worth having is
	// the one about the panic that caused this shutdown, and it is sitting in
	// a queue that dies with the process -- but an operator killing mcpd
	// should not wait on a collector that is unreachable, so five seconds is
	// the whole budget and missing the window is a warning rather than an
	// error. Nothing here has anything to do with the customer's own data
	// being safe; that was step 4.
	a.errors.Flush(5 * time.Second)

	return errors.Join(errs...)
}

// pruneHistory removes history past its retention, once a day.
//
// It reads the setting on every pass rather than at startup, so changing the
// retention takes effect without a restart -- and so shortening it actually
// shortens it, instead of taking effect the next time someone remembers to
// restart mcpd.
func (a *App) pruneHistory(ctx context.Context) error {
	const every = 24 * time.Hour

	// A first pass shortly after start, because a deployment that restarts
	// daily would otherwise never reach the tick.
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
		timer.Reset(every)

		days := a.settings.Int(ctx, settings.KeyHistoryRetentionDays, 7)
		if days <= 0 {
			continue
		}
		cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
		removed, err := a.audit.Prune(ctx, "system:retention", cutoff, time.Now())
		if err != nil {
			a.log.WarnContext(ctx, "could not prune history", "error", err)
			continue
		}
		if removed > 0 {
			a.log.InfoContext(ctx, "pruned history", "removed", removed, "older_than_days", days)
		}
	}
}

// announceServing closes a.serving once the MCP listener answers a request.
//
// It probes the listener's own address rather than the public URL: what needs
// establishing is that this process is serving, and going out through whatever
// NAT or proxy sits in front would test that too.
func (a *App) announceServing(ctx context.Context, addr net.Addr) {
	scheme := "http"
	client := &http.Client{Timeout: 2 * time.Second}
	if a.tls != nil {
		scheme = "https"
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(a.tls.CAPEM)
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}
	}

	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		close(a.serving)
		return
	}
	// A wildcard bind is not an address to dial.
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	url := scheme + "://" + net.JoinHostPort(host, port) + "/health/live"

	for attempt := 0; attempt < 100; attempt++ {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			close(a.serving)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
	// Give up waiting rather than holding the tunnel back for ever: it will
	// report its own failure, which is more use than silence.
	a.log.WarnContext(ctx, "the MCP listener did not answer; starting tunnels anyway")
	close(a.serving)
}
