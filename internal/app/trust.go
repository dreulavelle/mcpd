package app

import (
	"context"
	"crypto/x509"
	"net/http"
	"sync"
)

// The certificates an operator adds are trusted by every outbound connection a
// plugin makes, on top of the system roots.
//
// Everything, rather than a set each integration opts into. Naming the
// certificate again on each plugin that needs it is a second step with the
// same failure at the end of it -- the certificate is stored, the handshake
// still fails, and the error is the one the operator was trying to fix. A
// company authority in an operating system's trust store works this way, and
// that is the arrangement an administrator adding one is already working from.
//
// The pool is cached rather than rebuilt per plugin: it is read on every
// instance build, and a database round trip in that path would be paid on
// every reconcile for a set that changes when somebody changes it.
type trustPool struct {
	mu   sync.RWMutex
	pool *x509.CertPool
}

func (t *trustPool) get() *x509.CertPool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.pool
}

func (t *trustPool) set(pool *x509.CertPool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pool = pool
}

// loadTrustPool reads the stored certificates and caches the pool built from
// them.
//
// A failure here is logged and leaves the previous pool in place. The
// alternative -- dropping to the system roots because one read failed -- would
// withdraw trust from the internal upstreams that were working a moment ago,
// which is a worse outcome than carrying on with what was already loaded.
func (a *App) loadTrustPool(ctx context.Context) {
	pool, err := a.trust.Pool(ctx)
	if err != nil {
		a.log.ErrorContext(ctx, "could not build the certificate pool; "+
			"keeping the one already loaded", "error", err)
		return
	}
	a.trustPool.set(pool)

	certs, err := a.trust.List(ctx)
	if err != nil || len(certs) == 0 {
		return
	}
	names := make([]string, 0, len(certs))
	for _, c := range certs {
		names = append(names, c.Name)
	}
	a.log.InfoContext(ctx, "trusting extra certificates on top of the system roots",
		"count", len(names), "certificates", names)
}

// trustChanged applies an added or removed certificate without a restart.
//
// Remounting is what makes it take effect. A plugin holds the HTTP client it
// was built with, so a pool rebuilt underneath it would be believed by
// everything constructed afterwards and nothing already running -- which is
// the same "I added it and nothing happened" this feature exists to avoid.
// Reconciling is how every other setting change already applies, and a plugin
// rebuild is cheap next to an operator wondering whether it worked.
func (a *App) trustChanged(ctx context.Context) {
	a.loadTrustPool(ctx)

	names := make([]string, 0)
	for _, inst := range a.instances(ctx) {
		names = append(names, inst.Name)
	}
	if len(names) == 0 {
		return
	}
	a.log.InfoContext(ctx, "remounting plugins to pick up the new certificates",
		"plugins", names)
	a.reconcileDetached(names...)
}

// pluginHTTPClient builds the client one plugin reaches upstream with, on the
// trust this host currently has.
func (a *App) pluginHTTPClient() *http.Client {
	return newPluginHTTPClient(a.trustPool.get())
}
