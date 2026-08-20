package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Run starts every component and blocks until ctx is cancelled, then shuts
// down in reverse order.
func (a *App) Run(ctx context.Context) error {
	if err := a.manager.Start(ctx); err != nil {
		return err
	}

	errCh := make(chan error, 1)
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
	if err := a.server.Shutdown(ctx); err != nil {
		errs = append(errs, fmt.Errorf("app: drain http server: %w", err))
	}
	a.log.Info("http server drained")

	// 2. Stop plugins, in reverse registration order.
	pluginCtx, pluginCancel := context.WithTimeout(ctx, 10*time.Second)
	if err := a.manager.Shutdown(pluginCtx); err != nil {
		errs = append(errs, err)
	}
	pluginCancel()
	a.log.Info("plugins stopped")

	// 3. Close the database last, checkpointing the WAL so the file is
	//    self-contained for backup on exit.
	if err := a.db.Close(); err != nil {
		errs = append(errs, fmt.Errorf("app: close database: %w", err))
	}
	a.log.Info("database closed")

	return errors.Join(errs...)
}
