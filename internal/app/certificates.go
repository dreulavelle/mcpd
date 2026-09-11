package app

import (
	"context"
	"errors"
	stdlog "log"
	"log/slog"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/spoked/mcpd/internal/admin"
	"github.com/spoked/mcpd/internal/config"
	"github.com/spoked/mcpd/internal/servertls"
	"github.com/spoked/mcpd/internal/settings"
)

// certificateCheckInterval is how often mcpd's own certificate is looked at
// while it runs, besides whenever an address it covers changes. Renewal is a
// month before expiry, so this only has to be frequent enough not to miss a
// month.
const certificateCheckInterval = 24 * time.Hour

// providedCheckInterval is how often the uploaded certificate's file is
// looked at for a replacement written by something outside mcpd. A stat, so
// it costs nothing, and a renewal lands within a minute.
const providedCheckInterval = time.Minute

// expiringSoon is when a certificate somebody has to replace by hand starts
// being mentioned: long enough to get a reissue through an authority's queue.
const expiringSoon = 30 * 24 * time.Hour

// Dashboard certificate modes, as the setting stores them.
const (
	dashboardTLSOff      = "off"
	dashboardTLSOwn      = "self-signed"
	dashboardTLSProvided = "custom"
)

// certificates is what the two listeners present.
//
// mcpd's own certificate is one certificate covering every address a
// listener that uses it is reached at. Two would mean two leaves written to
// the same files, each reissuing over the other on every start. The dashboard
// may present that one, or a certificate somebody uploaded instead -- one
// from their company's authority, which their computers already trust.
type certificates struct {
	dir string
	// assistants and dashboardMode are what the settings asked for when this
	// process started. Both are ApplyRestart: a listener is built once.
	assistants     bool
	dashboardMode  string
	listen         string
	frontendListen string

	// own is mcpd's own certificate: nil when neither listener asked for it,
	// or when issuing it failed.
	own *servertls.Holder
	// dash is what the dashboard presents -- own, or an uploaded one -- and
	// nil when the dashboard is on plain http.
	dash *servertls.Holder
	// provided reports that dash holds an uploaded certificate.
	provided bool
	// problem is why the dashboard is on plain http although it asked for
	// https. The dashboard is the page this would be fixed on, so failing to
	// load its certificate is a reason to serve it without one rather than a
	// reason not to start.
	problem error
	// wake asks the renewal worker to look now rather than tomorrow.
	wake chan struct{}

	mu          sync.Mutex
	providedMod time.Time
}

// dashboard reports whether the dashboard serves https.
func (c *certificates) dashboard() bool { return c.dash != nil }

// ownHosts is every address mcpd's own certificate has to cover, read from
// the addresses as they are now: an address changed while mcpd runs is
// covered from the next check, not the next restart.
func (a *App) ownHosts(ctx context.Context) []string {
	c := a.certs
	var hosts []string
	if c.assistants {
		hosts = append(hosts, servertls.HostsFor(a.publicURL(ctx), c.listen)...)
	}
	if c.dashboardMode == dashboardTLSOwn {
		hosts = append(hosts, servertls.HostsFor(a.frontendPublicURL(ctx), c.frontendListen)...)
	}
	return hosts
}

// setupCertificates loads or issues what each listener asked to present.
//
// A failure is fatal only for mcpd's own certificate when the MCP listener
// asked for it, which is the behaviour that listener has always had: a
// connector configured for https has nothing to fall back to. The dashboard
// falls back to plain http and says why.
func (a *App) setupCertificates(ctx context.Context, cfg *config.Config, boot startup, log *slog.Logger) error {
	mode := boot.frontendTLSMode
	if !boot.frontendEnabled || mode == "" {
		mode = dashboardTLSOff
	}
	c := &certificates{
		dir:            cfg.TLSDir(),
		assistants:     boot.tlsSelfSigned,
		dashboardMode:  mode,
		listen:         cfg.Server.Listen,
		frontendListen: cfg.Server.FrontendListen,
		wake:           make(chan struct{}, 1),
	}
	a.certs = c
	now := time.Now()

	if c.assistants || mode == dashboardTLSOwn {
		m, err := servertls.EnsureSelfSigned(c.dir, a.ownHosts(ctx), now)
		switch {
		case err != nil && c.assistants:
			return err
		case err != nil:
			c.problem = err
			log.ErrorContext(ctx, "could not make a certificate for the dashboard; "+
				"serving it over plain http", "error", err)
		default:
			c.own = servertls.NewHolder(m)
			log.InfoContext(ctx, "serving https with mcpd's own certificate",
				"dashboard", mode == dashboardTLSOwn,
				"assistants", c.assistants,
				"hosts", m.Hosts,
				"expires", m.NotAfter.Format(time.RFC3339),
				"issued_now", m.Issued,
				"ca", m.CAPath)
			if mode == dashboardTLSOwn {
				c.dash = c.own
			}
		}
	}

	if mode == dashboardTLSProvided {
		m, err := servertls.LoadProvided(c.dir, now)
		switch {
		case errors.Is(err, servertls.ErrNoProvided):
			c.problem = err
			log.WarnContext(ctx, "the dashboard is set to use an uploaded certificate, but "+
				"none has been uploaded; serving it over plain http until one is and mcpd restarts")
		case err != nil:
			c.problem = err
			log.ErrorContext(ctx, "could not load the dashboard's certificate; "+
				"serving it over plain http", "error", err)
		default:
			c.dash = servertls.NewHolder(m)
			c.provided = true
			c.providedMod = modTime(servertls.ProvidedPath(c.dir))
			log.InfoContext(ctx, "serving https with an uploaded certificate",
				"subject", m.Subject, "issuer", m.Issuer, "hosts", m.Hosts,
				"expires", m.NotAfter.Format(time.RFC3339))
		}
	}

	if c.own != nil {
		// An address mcpd's own certificate covers has changed. Looked at
		// from the worker rather than here: a watcher runs inside the
		// settings write and must not do file work there.
		a.settings.Watch(func(changed []string) {
			if slices.Contains(changed, settings.KeyServerPublicURL) ||
				slices.Contains(changed, settings.KeyServerFrontendPublicURL) {
				select {
				case c.wake <- struct{}{}:
				default:
				}
			}
		})
	}
	return nil
}

// certificatesNeedWatching reports whether the renewal worker has anything to
// do.
func (a *App) certificatesNeedWatching() bool {
	return a.certs != nil && (a.certs.own != nil || a.certs.provided)
}

// renewCertificates keeps what the listeners present current while mcpd
// runs. mcpd's own certificate is reissued a month before it expires, and
// whenever an address it covers changes; an uploaded one is reloaded when its
// file is replaced on disk. A connection made afterwards gets the new one;
// nothing restarts.
func (a *App) renewCertificates(ctx context.Context) error {
	c := a.certs
	ticker := time.NewTicker(providedCheckInterval)
	defer ticker.Stop()
	lastOwn := time.Now()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		case <-c.wake:
			lastOwn = time.Time{}
		}
		if c.own != nil && time.Since(lastOwn) >= certificateCheckInterval {
			a.renewOwn(ctx)
			lastOwn = time.Now()
		}
		if c.provided {
			a.reloadProvided(ctx)
		}
	}
}

func (a *App) renewOwn(ctx context.Context) {
	c := a.certs
	m, err := servertls.EnsureSelfSigned(c.dir, a.ownHosts(ctx), time.Now())
	if err != nil {
		// The certificate in use is still in use. It stays valid until its
		// date, and tomorrow's check tries again well before then.
		a.log.ErrorContext(ctx, "could not renew mcpd's own certificate; "+
			"keeping the current one", "error", err,
			"expires", c.own.Materials().NotAfter.Format(time.RFC3339))
		return
	}
	c.own.Store(m)
	if m.Issued {
		a.log.InfoContext(ctx, "renewed mcpd's own certificate",
			"hosts", m.Hosts, "expires", m.NotAfter.Format(time.RFC3339))
	}
}

// reloadProvided serves a replacement written to the uploaded certificate's
// file by something outside mcpd: a renewal script, or an operator.
func (a *App) reloadProvided(ctx context.Context) {
	c := a.certs
	mod := modTime(servertls.ProvidedPath(c.dir))
	c.mu.Lock()
	unchanged := mod.IsZero() || mod.Equal(c.providedMod)
	c.mu.Unlock()
	if unchanged {
		// Removed or unreadable is not a reason to stop serving what is
		// loaded; the next restart says so.
		return
	}
	m, err := servertls.LoadProvided(c.dir, time.Now())
	c.mu.Lock()
	c.providedMod = mod
	c.mu.Unlock()
	if err != nil {
		a.log.ErrorContext(ctx, "the dashboard's certificate file changed but cannot be "+
			"served; keeping the one in use", "error", err)
		return
	}
	c.dash.Store(m)
	a.log.InfoContext(ctx, "the dashboard's certificate was replaced on disk; serving the new one",
		"subject", m.Subject, "issuer", m.Issuer, "hosts", m.Hosts,
		"expires", m.NotAfter.Format(time.RFC3339))
}

// setDashboardCertificate installs an uploaded certificate for the
// dashboard. When the dashboard is already serving an uploaded one it swaps
// at once; otherwise it is served from the restart that turns it on.
func (a *App) setDashboardCertificate(ctx context.Context, actor string, cert, key []byte) (admin.TLSStatus, error) {
	c := a.certs
	m, err := servertls.StoreProvided(c.dir, time.Now(), cert, key)
	if err != nil {
		return admin.TLSStatus{}, err
	}
	if c.provided {
		c.dash.Store(m)
		c.mu.Lock()
		c.providedMod = modTime(servertls.ProvidedPath(c.dir))
		c.mu.Unlock()
	}
	a.log.InfoContext(ctx, "dashboard certificate uploaded",
		"by", actor, "subject", m.Subject, "issuer", m.Issuer, "hosts", m.Hosts,
		"expires", m.NotAfter.Format(time.RFC3339), "fingerprint", m.Fingerprint,
		"in_use_now", c.provided)
	return a.tlsStatus(ctx), nil
}

// removeDashboardCertificate deletes the uploaded certificate, unless the
// dashboard is serving it: removing that would leave the next restart with
// nothing to serve.
func (a *App) removeDashboardCertificate(ctx context.Context, actor string) error {
	c := a.certs
	if c.provided {
		return admin.ErrCertificateInUse
	}
	if err := servertls.RemoveProvided(c.dir); err != nil {
		return err
	}
	a.log.InfoContext(ctx, "dashboard certificate removed", "by", actor)
	return nil
}

// dashboardServesTLS reports whether the dashboard was asked to serve https
// itself, whether or not it managed to. The session cookie reads it.
func (a *App) dashboardServesTLS() bool {
	return a.certs != nil && a.certs.dashboardMode != dashboardTLSOff
}

// caPEM is the authority that signed mcpd's own certificate, for an operator
// to install. Nil when there is none.
func (a *App) caPEM() []byte {
	if a.certs == nil || a.certs.own == nil {
		return nil
	}
	return a.certs.own.Materials().CAPEM
}

// tlsStatus says what each listener presents, what stands in the way of one
// that asked to, and what browsers will object to.
func (a *App) tlsStatus(ctx context.Context) admin.TLSStatus {
	out := admin.TLSStatus{
		DashboardMode: a.settings.FieldString(ctx, settings.KeyServerFrontendTLSMode),
	}
	c := a.certs
	if c == nil {
		return out
	}
	now := time.Now()

	if c.own != nil {
		m := c.own.Materials()
		var warnings []string
		if c.dash == c.own {
			warnings = a.coverageWarnings(ctx, m)
		}
		out.Own = certificateInfo(m, warnings)
		out.Authority = len(m.CAPEM) > 0
		out.Assistants = admin.ListenerTLS{On: c.assistants}
		if c.assistants {
			out.Assistants.Source = "own"
		}
	}

	// The uploaded certificate, served or not: the page shows what is there
	// so it can be checked before the restart that starts serving it.
	var provided *servertls.Materials
	if c.provided {
		provided = c.dash.Materials()
	} else if m, err := servertls.LoadProvided(c.dir, now); err == nil {
		provided = m
	}
	if provided != nil {
		out.Provided = certificateInfo(provided, a.providedWarnings(ctx, provided, now))
	}

	out.Dashboard.On = c.dashboard()
	switch {
	case c.provided:
		out.Dashboard.Source = "provided"
	case c.dashboard():
		out.Dashboard.Source = "own"
	}
	if c.problem != nil && !c.dashboard() {
		switch {
		case errors.Is(c.problem, servertls.ErrNoProvided) && provided != nil:
			// Uploaded since this process started; restart_needed says the rest.
		case errors.Is(c.problem, servertls.ErrNoProvided):
			out.Dashboard.Problem = "The dashboard is set to use your own certificate, but none " +
				"had been uploaded when mcpd started, so it is on plain http. Upload it below, " +
				"then restart mcpd."
		default:
			out.Dashboard.Problem = "The dashboard was set to https, but mcpd couldn't load its " +
				"certificate, so it is on plain http. Check that the data folder can be read " +
				"and written to, then restart."
			out.Dashboard.Detail = c.problem.Error()
		}
	}

	switch {
	case out.DashboardMode != "" && out.DashboardMode != c.dashboardMode && c.frontendListen != "":
		out.RestartNeeded = true
	case out.DashboardMode == dashboardTLSProvided && !c.provided && provided != nil:
		out.RestartNeeded = true
	}
	return out
}

// coverageWarnings is what browsers will say about mcpd's own certificate at
// the address this page is on. It covers what it is told to, so the only gap
// is an address it has not been told yet.
func (a *App) coverageWarnings(ctx context.Context, m *servertls.Materials) []string {
	host := hostOf(a.frontendPublicURL(ctx))
	switch {
	case host == "":
		return []string{"The certificate covers only this machine. Fill in Address this page " +
			"is on, and it will cover that address too."}
	case !coversHost(m, host):
		// Only until the worker's next look, which a change to the address
		// triggers at once.
		return []string{"The certificate doesn't cover " + host + " yet. It is being renewed " +
			"to cover it."}
	}
	return nil
}

// providedWarnings is what browsers will say about an uploaded certificate.
// Nothing renews it, so running out is said early.
func (a *App) providedWarnings(ctx context.Context, m *servertls.Materials, now time.Time) []string {
	var out []string
	if host := hostOf(a.frontendPublicURL(ctx)); host != "" && !coversHost(m, host) {
		out = append(out, "This certificate doesn't cover "+host+", the address this page "+
			"is on, so browsers will refuse it there. It covers "+strings.Join(m.Hosts, ", ")+".")
	}
	switch {
	case now.After(m.NotAfter):
		out = append(out, "This certificate ran out on "+day(m.NotAfter)+", so browsers "+
			"refuse it. Upload its replacement.")
	case m.NotAfter.Sub(now) < expiringSoon:
		out = append(out, "This certificate runs out on "+day(m.NotAfter)+". Upload its "+
			"replacement before then.")
	}
	if now.Before(m.NotBefore) {
		out = append(out, "This certificate isn't valid until "+day(m.NotBefore)+".")
	}
	if m.CloudflareOrigin {
		out = append(out, "This is a Cloudflare Origin certificate. Browsers trust it only when "+
			"this dashboard is reached through Cloudflare, so reaching it directly shows a warning.")
	}
	return out
}

func certificateInfo(m *servertls.Materials, warnings []string) *admin.CertificateInfo {
	return &admin.CertificateInfo{
		Subject:     m.Subject,
		Issuer:      m.Issuer,
		Hosts:       m.Hosts,
		NotBefore:   m.NotBefore.UTC().Format(time.RFC3339),
		NotAfter:    m.NotAfter.UTC().Format(time.RFC3339),
		Fingerprint: m.Fingerprint,
		Warnings:    warnings,
	}
}

// coversHost reports whether a browser at host would accept the certificate
// by name. VerifyHostname rather than a list lookup, so a wildcard -- which
// most certificates bought for a domain are -- is honoured.
func coversHost(m *servertls.Materials, host string) bool {
	return m.Certificate.Leaf != nil && m.Certificate.Leaf.VerifyHostname(host) == nil
}

func day(t time.Time) string { return t.UTC().Format("2 January 2006") }

func modTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// hostOf is the host a URL names, lowercased the way certificate hosts are.
func hostOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// serverErrorLog is where net/http reports what goes wrong beneath a handler,
// at warn -- except a failed TLS handshake, which is debug.
//
// With mcpd's own certificate, every browser that has not been given the
// authority yet refuses the handshake on every connection, several to a page.
// A warning for each buries the log under something expected, understood, and
// fixed on the client rather than here, and a real problem logged beside them
// is the thing nobody then sees. Turning debug on still shows each one.
func serverErrorLog(log *slog.Logger) *stdlog.Logger {
	return stdlog.New(handshakeQuieter{log}, "", 0)
}

type handshakeQuieter struct{ log *slog.Logger }

func (q handshakeQuieter) Write(p []byte) (int, error) {
	msg := strings.TrimSpace(string(p))
	level := slog.LevelWarn
	if strings.HasPrefix(msg, "http: TLS handshake error") {
		level = slog.LevelDebug
	}
	// No context to log with: net/http gives its error log none.
	q.log.Log(context.Background(), level, msg)
	return len(p), nil
}
