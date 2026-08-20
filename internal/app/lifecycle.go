package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"syscall"
	"time"

	"github.com/spoked/mcpd/internal/auth/oauth"
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
			a.log.Error("background worker stopped unexpectedly", "worker", name, "error", err)
			return
		}
		a.log.Debug("background worker stopped", "worker", name)
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
	a.log.Info("resuming approved operations left from a previous run", "count", len(pending))
	for _, op := range pending {
		if ctx.Err() != nil {
			return nil
		}
		if err := a.executor.Execute(ctx, op.ID); err != nil {
			a.log.Error("failed to resume an operation", "operation_id", op.ID, "error", err)
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
	if a.oauthStore != nil {
		hk := oauth.NewHousekeeper(a.oauthStore, time.Hour, 7*24*time.Hour)
		a.startWorker("oauth-housekeeper", workerCtx, hk.Run)
	}

	// Anything approved while the process was down still needs executing. The
	// event announcing it was consumed, or never delivered, so a startup scan
	// is what makes the executor restart-safe.
	a.startWorker("claimable-scan", workerCtx, a.scanClaimable)

	errCh := make(chan error, 2)

	if a.frontend != nil {
		go func() {
			a.log.Info("dashboard listening", "addr", a.frontend.Addr)
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

	go func() {
		a.log.Info("http server listening",
			"addr", a.cfg.Server.Listen,
			"public_url", a.cfg.Server.PublicURL,
			"plugins", a.manager.Names())
		if err := a.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
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
	case <-ctx.Done():
		a.log.Info("shutdown signal received")
		return a.Shutdown()
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
	ctx, cancel := context.WithTimeout(context.Background(), a.cfg.Server.ShutdownTimeout)
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

	return errors.Join(errs...)
}
