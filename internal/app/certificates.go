package app

import (
	"context"
	stdlog "log"
	"log/slog"
	"net/url"
	"slices"
	"strings"
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

// certificates is mcpd's own certificate, and which listeners present it.
//
// One certificate for both listeners, covering every address either is
// reached at. Two would mean two leaves written to the same files, each
// reissuing over the other on every start.
type certificates struct {
	dir string
	// assistants and dashboard are what the settings asked for when this
	// process started. Both are ApplyRestart: a listener is built once.
	assistants, dashboard  bool
	listen, frontendListen string

	// holder is nil when neither listener asked, or when issuing failed.
	holder *servertls.Holder
	// problem is why the dashboard is on plain http although it asked for
	// https. The dashboard is the page this would be fixed on, so failing to
	// make its certificate is a reason to serve it without one rather than a
	// reason not to start.
	problem error
	// wake asks the renewal worker to look now rather than tomorrow.
	wake chan struct{}
}

// hosts is every address the certificate has to cover, read from the
// addresses as they are now: an address changed while mcpd runs is covered
// from the next check, not the next restart.
func (a *App) certificateHosts(ctx context.Context) []string {
	c := a.certs
	var hosts []string
	if c.assistants {
		hosts = append(hosts, servertls.HostsFor(a.publicURL(ctx), c.listen)...)
	}
	if c.dashboard {
		hosts = append(hosts, servertls.HostsFor(a.frontendPublicURL(ctx), c.frontendListen)...)
	}
	return hosts
}

// setupCertificates issues or loads mcpd's own certificate for whichever
// listeners asked for it.
//
// A failure is fatal only when the MCP listener asked, which is the behaviour
// that listener has always had: a connector configured for https has nothing
// to fall back to. The dashboard falls back to plain http and says why.
func (a *App) setupCertificates(ctx context.Context, cfg *config.Config, boot startup, log *slog.Logger) error {
	a.certs = &certificates{
		dir:            cfg.TLSDir(),
		assistants:     boot.tlsSelfSigned,
		dashboard:      boot.frontendTLS && boot.frontendEnabled,
		listen:         cfg.Server.Listen,
		frontendListen: cfg.Server.FrontendListen,
		wake:           make(chan struct{}, 1),
	}
	c := a.certs
	if !c.assistants && !c.dashboard {
		return nil
	}

	materials, err := servertls.EnsureSelfSigned(c.dir, a.certificateHosts(ctx), time.Now())
	if err != nil {
		if c.assistants {
			return err
		}
		c.problem = err
		c.dashboard = false
		log.ErrorContext(ctx, "could not make a certificate for the dashboard; "+
			"serving it over plain http", "error", err)
		return nil
	}
	c.holder = servertls.NewHolder(materials)
	log.InfoContext(ctx, "serving https with mcpd's own certificate",
		"dashboard", c.dashboard,
		"assistants", c.assistants,
		"hosts", materials.Hosts,
		"expires", materials.NotAfter.Format(time.RFC3339),
		"issued_now", materials.Issued,
		"ca", materials.CAPath)

	// An address the certificate covers has changed. Looked at from the
	// worker rather than here: a watcher runs inside the settings write and
	// must not do file work there.
	a.settings.Watch(func(changed []string) {
		if slices.Contains(changed, settings.KeyServerPublicURL) ||
			slices.Contains(changed, settings.KeyServerFrontendPublicURL) {
			select {
			case c.wake <- struct{}{}:
			default:
			}
		}
	})
	return nil
}

// renewCertificates keeps mcpd's own certificate current while it runs:
// reissued a month before it expires, and whenever an address it has to cover
// changes. A connection made after a renewal gets the new one; nothing
// restarts.
func (a *App) renewCertificates(ctx context.Context) error {
	c := a.certs
	ticker := time.NewTicker(certificateCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		case <-c.wake:
		}
		m, err := servertls.EnsureSelfSigned(c.dir, a.certificateHosts(ctx), time.Now())
		if err != nil {
			// The certificate in use is still in use. It stays valid until
			// its date, and tomorrow's check tries again well before then.
			a.log.ErrorContext(ctx, "could not renew mcpd's own certificate; "+
				"keeping the current one", "error", err,
				"expires", c.holder.Materials().NotAfter.Format(time.RFC3339))
			continue
		}
		c.holder.Store(m)
		if m.Issued {
			a.log.InfoContext(ctx, "renewed mcpd's own certificate",
				"hosts", m.Hosts, "expires", m.NotAfter.Format(time.RFC3339))
		}
	}
}

// caPEM is the authority that signed mcpd's own certificate, for an operator
// to install. Nil when there is none.
func (a *App) caPEM() []byte {
	if a.certs == nil || a.certs.holder == nil {
		return nil
	}
	return a.certs.holder.Materials().CAPEM
}

// tlsStatus says which listeners are serving mcpd's own certificate, and what
// stands in the way of one that asked to.
func (a *App) tlsStatus(ctx context.Context) admin.TLSStatus {
	c := a.certs
	out := admin.TLSStatus{Hosts: []string{}}
	if c == nil {
		return out
	}
	out.Assistants.On = c.assistants && c.holder != nil
	out.Dashboard.On = c.dashboard && c.holder != nil
	if c.problem != nil {
		out.Dashboard.Problem = "The dashboard was set to https, but mcpd couldn't make its " +
			"certificate, so it is on plain http. Check that the data folder can be written to, " +
			"then restart."
		out.Dashboard.Detail = c.problem.Error()
	}
	if c.holder == nil {
		return out
	}
	m := c.holder.Materials()
	out.Hosts = m.Hosts
	out.Expires = m.NotAfter.UTC().Format(time.RFC3339)
	out.Authority = len(m.CAPEM) > 0

	// Covered or not is worth saying. A certificate for localhost alone is
	// valid, and every browser on the network still refuses it.
	if out.Dashboard.On {
		host := hostOf(a.frontendPublicURL(ctx))
		switch {
		case host == "":
			out.Dashboard.Warning = "The certificate covers only this machine. Fill in " +
				"Address this page is on, and it will cover that address too."
		case !slices.Contains(m.Hosts, host):
			// Only until the worker's next look, which a change to the
			// address triggers at once.
			out.Dashboard.Warning = "The certificate doesn't cover " + host + " yet. " +
				"It is being renewed to cover it."
		}
	}
	return out
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
