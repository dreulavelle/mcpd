package app

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/config"
	"github.com/spoked/mcpd/internal/observability"
	"github.com/spoked/mcpd/internal/settings"
)

// Where configuration lives, and which of the two places wins.
//
// Most of what used to be in config.yaml is in the settings table now, and for
// those keys the database is the only authority: the file is not consulted to
// run, and neither is the MCPD_ override that used to apply to it. What the
// file and the environment still get is one turn. On the first start after an
// upgrade -- and on the first start of a new deployment, which is the same
// code path -- whatever they supply for a moved key is imported into the store,
// once, attributed to configImportActor and recorded in settings_history like
// any other change. After that they are ignored, and any key still naming a
// value that disagrees with the stored one is named in a startup warning.
//
// One authority and a warning, rather than a precedence chain. A chain means
// two sources can disagree indefinitely with nothing saying so, which is how
// an operator comes to edit a file for an hour and change nothing.
//
// The reason for moving them at all is not tidiness. Editing config.yaml
// leaves no record: no actor, no before, no after, and nothing the dashboard
// can show. A settings change has all four.

// configImportActor is who the one-time import is recorded as.
//
// Not the operator who happened to restart the host: they did not choose these
// values, they inherited them, and attributing the change to them would put a
// decision in the trail that nobody made.
const configImportActor = "system:config-import"

// movedSetting is one value config.yaml used to supply and the settings store
// now owns.
type movedSetting struct {
	// file is where it was written, for the messages an operator reads.
	file string
	// key is the setting that owns it now.
	key string
	// kind decides how the value is encoded and validated. Most of these are
	// declared fields and could look it up; the handful that are deliberately
	// not fields -- the tunnel id, the principal, the control plane override --
	// could not, so it is stated here for all of them.
	kind settings.Kind
	// value is the value in the form the kind takes: a whole number of the
	// field's own unit for a duration, a comma-separated list, "true" or
	// "false" for a switch.
	value string
	// secret marks a value to encrypt at rest.
	secret bool
}

// movedSettings lists what the file, or an MCPD_ override, still supplies for
// a key that has moved.
//
// Only what is actually supplied. A file that says nothing about read_timeout
// has nothing to import and nothing to warn about, which is why config.Legacy
// holds pointers.
func movedSettings(l *config.Legacy) []movedSetting {
	var out []movedSetting

	add := func(file, key string, kind settings.Kind, value string) {
		out = append(out, movedSetting{file: file, key: key, kind: kind, value: value})
	}
	str := func(file, key string, v *string) {
		if v != nil {
			add(file, key, settings.KindString, *v)
		}
	}
	enum := func(file, key string, v *string, empty string) {
		if v == nil {
			return
		}
		value := strings.TrimSpace(*v)
		if value == "" {
			// An absent enum in the file meant something -- "no certificate",
			// "no inline approval at all" -- and a dropdown has to be able to
			// say it.
			value = empty
		}
		add(file, key, settings.KindEnum, value)
	}
	boolean := func(file, key string, v *bool) {
		if v != nil {
			add(file, key, settings.KindBool, strconv.FormatBool(*v))
		}
	}
	dur := func(file, key string, v *time.Duration, unit time.Duration) {
		if v != nil {
			add(file, key, settings.KindDuration, strconv.Itoa(int(*v/unit)))
		}
	}
	list := func(file, key string, v *[]string) {
		if v != nil {
			add(file, key, settings.KindList, strings.Join(*v, ","))
		}
	}

	str("server.public_url", settings.KeyServerPublicURL, l.Server.PublicURL)
	str("server.frontend_public_url", settings.KeyServerFrontendPublicURL, l.Server.FrontendPublicURL)
	enum("server.tls.mode", settings.KeyServerTLSMode, l.Server.TLS.Mode, "off")
	boolean("server.frontend_enabled", settings.KeyServerFrontendEnabled, l.Server.FrontendEnabled)
	dur("server.read_header_timeout", settings.KeyServerReadHeaderTimeout, l.Server.ReadHeaderTimeout, time.Second)
	dur("server.read_timeout", settings.KeyServerReadTimeout, l.Server.ReadTimeout, time.Second)
	dur("server.write_timeout", settings.KeyServerWriteTimeout, l.Server.WriteTimeout, time.Second)
	dur("server.idle_timeout", settings.KeyServerIdleTimeout, l.Server.IdleTimeout, time.Second)
	dur("server.shutdown_timeout", settings.KeyServerShutdownTimeout, l.Server.ShutdownTimeout, time.Second)

	dur("storage.busy_timeout", settings.KeyStorageBusyTimeout, l.Storage.BusyTimeout, time.Second)
	boolean("storage.relaxed_durability", settings.KeyStorageRelaxedDurability, l.Storage.RelaxedDurability)

	dur("auth.accounts.session_ttl", settings.KeyAccountsSessionTTL, l.Auth.Accounts.SessionTTL, time.Hour)

	dur("approval.proposal_ttl", settings.KeyApprovalProposalTTL, l.Approval.ProposalTTL, time.Minute)
	dur("approval.approval_ttl", settings.KeyApprovalApprovalTTL, l.Approval.ApprovalTTL, time.Minute)
	dur("approval.lease_ttl", settings.KeyApprovalLeaseTTL, l.Approval.LeaseTTL, time.Minute)
	enum("approval.inline_max_risk", settings.KeyApprovalInlineMaxRisk, l.Approval.InlineMaxRisk, settings.RiskNone)

	enum("logging.level", settings.KeyLoggingLevel, l.Logging.Level, "info")
	enum("logging.format", settings.KeyLoggingFormat, l.Logging.Format, "json")

	boolean("tunnel.enabled", settings.KeyTunnelEnabled, l.Tunnel.Enabled)
	str("tunnel.tunnel_id", settings.KeyTunnelID, l.Tunnel.TunnelID)
	str("tunnel.principal", settings.KeyTunnelPrincipal, l.Tunnel.Principal)
	enum("tunnel.role", settings.KeyTunnelRole, l.Tunnel.Role, "user")
	list("tunnel.plugins", settings.KeyTunnelPlugins, l.Tunnel.Plugins)
	str("tunnel.control_plane_base_url", settings.KeyTunnelControlPlane, l.Tunnel.ControlPlaneBaseURL)
	str("tunnel.diagnostics_addr", settings.KeyTunnelDiagnostics, l.Tunnel.DiagnosticsAddr)
	boolean("tunnel.check_for_updates", settings.KeyTunnelUpdates, l.Tunnel.CheckForUpdates)

	return out
}

// importRecord is what is written under settings.KeyConfigImported.
//
// The store rather than only a log line, because "where did this value come
// from" is a question asked long after the log has rotated.
type importRecord struct {
	At       time.Time `json:"at"`
	Imported []string  `json:"imported"`
	// Kept names the keys the file supplied that the store already had a value
	// for. Those were not overwritten: a value somebody set in the dashboard
	// outranks the one the deployment was started with.
	Kept []string `json:"kept,omitempty"`
	// Refused names the keys whose file value the settings schema rejects.
	// They are left out rather than stored, so a value the dashboard would
	// refuse to accept cannot arrive through the back door.
	Refused []string `json:"refused,omitempty"`
}

// importLegacyConfig seeds the settings store from the startup file, once.
//
// It never overwrites. A key the store already holds keeps its value, because
// a value in the store was chosen by somebody and the file's was inherited.
//
// It reports whether this is the start that did the importing, because that
// start is the one on which nothing can be stale: the store holds what the
// file supplied because it was just put there.
func (a *App) importLegacyConfig(ctx context.Context) (imported bool, err error) {
	legacy := a.cfg.Legacy()

	if _, done, err := a.settings.Get(ctx, settings.KeyConfigImported); err != nil {
		return false, err
	} else if done {
		return false, nil
	}

	var (
		changes  []settings.Change
		record   = importRecord{At: time.Now().UTC()}
		deferred bool
	)

	for _, m := range movedSettings(legacy) {
		if _, present, err := a.settings.Get(ctx, m.key); err != nil {
			return false, err
		} else if present {
			record.Kept = append(record.Kept, m.key)
			continue
		}
		if err := validateMoved(m); err != nil {
			a.log.WarnContext(ctx, "a value in the startup file is not one this host will accept, "+
				"so it was not imported",
				"key", m.file, "detail", err.Error())
			record.Refused = append(record.Refused, m.key)
			continue
		}
		stored := m.value
		if !m.secret {
			encoded, err := settings.Encode(m.kind, m.value)
			if err != nil {
				record.Refused = append(record.Refused, m.key)
				continue
			}
			stored = encoded
		}
		changes = append(changes, settings.Change{Key: m.key, Value: stored, Secret: m.secret})
		record.Imported = append(record.Imported, m.key)
	}

	// The tunnel's API key is the one moved value that is a credential rather
	// than a setting. The file named a reference -- env:OPENAI_TUNNEL_API_KEY
	// -- which is resolved here once and stored encrypted, so the key ends up
	// where every other credential this host holds already is.
	if ref := strings.TrimSpace(deref(legacy.Tunnel.APIKeyRef)); ref != "" {
		switch key, err := a.resolveTunnelKey(ctx, ref); {
		case err != nil:
			a.log.WarnContext(ctx, "the tunnel's API key could not be read from the reference in "+
				"the startup file, so it was not imported",
				"reference", ref, "error", err)
			record.Refused = append(record.Refused, settings.KeyTunnelAPIKey)
		case key == "":
			// Already stored, or nothing to store.
		case !a.settings.HasCipher():
			// Refusing to write it in the clear is the only correct answer,
			// and so is refusing to record the import as finished: the moment
			// a key is configured, the next start picks this up.
			a.log.WarnContext(ctx, "the startup file names a tunnel API key but this host has no "+
				"encryption key, so the key could not be moved into the database and "+
				"the tunnel will not connect. Set secret_key_ref and restart.")
			deferred = true
		default:
			changes = append(changes, settings.Change{
				Key: settings.KeyTunnelAPIKey, Value: key, Secret: true})
			record.Imported = append(record.Imported, settings.KeyTunnelAPIKey)
		}
	}

	sort.Strings(record.Imported)
	sort.Strings(record.Kept)
	sort.Strings(record.Refused)

	// The marker is what makes this the fast path next time. It is withheld
	// when something importable could not be imported yet, so the next start
	// tries again -- which is safe, because a key already in the store is
	// never overwritten and so is never imported twice.
	if !deferred {
		marker, err := json.Marshal(record)
		if err != nil {
			return false, fmt.Errorf("app: record the configuration import: %w", err)
		}
		changes = append(changes, settings.Change{
			Key: settings.KeyConfigImported, Value: string(marker)})
	}
	if len(changes) > 0 {
		if err := a.settings.Apply(ctx, configImportActor, changes); err != nil {
			return false, fmt.Errorf("app: import the startup file's settings: %w", err)
		}
	}
	if len(record.Imported) > 0 {
		attrs := []any{"imported", record.Imported}
		if len(record.Kept) > 0 {
			attrs = append(attrs, "kept", record.Kept)
		}
		attrs = append(attrs, "note",
			"these keys are no longer read from the startup file; "+
				"change them on the Settings page, and delete them from the file")
		a.log.InfoContext(ctx, "settings from the startup file were imported into the database, "+
			"which is where they live now", attrs...)
	}
	// An unfinished import is not one to stay quiet about: the warnings are
	// how an operator learns what is still not where it should be.
	return !deferred, nil
}

// resolveTunnelKey reads the tunnel API key the file references, unless the
// store already holds one.
func (a *App) resolveTunnelKey(ctx context.Context, ref string) (string, error) {
	if existing, ok, err := a.settings.Get(ctx, settings.KeyTunnelAPIKey); err != nil {
		return "", err
	} else if ok && existing != "" {
		return "", nil
	}
	return config.NewSecretResolver().Resolve(ref)
}

// validateMoved checks a file value against the schema that owns its key now.
//
// The same validator the dashboard uses, so a value the form would refuse
// cannot arrive by being written into a file instead. Keys that are
// deliberately not form fields have no declaration to check against, and are
// taken as they are.
func validateMoved(m movedSetting) error {
	if _, ok := settings.FieldFor(m.key); !ok {
		return nil
	}
	return settings.Validate(m.key, m.value)
}

// staleConfigWarnings names the keys the startup file still sets that mcpd is
// no longer reading, and that disagree with what it is running.
//
// Disagree, rather than merely being present. A container that keeps setting
// MCPD_PUBLIC_URL to the address the store already holds is not a problem and
// should not be told it has one; a file saying 90s where the host is running
// 60s is exactly the silent disagreement this arrangement exists to remove.
func (a *App) staleConfigWarnings(ctx context.Context) []string {
	sources := a.cfg.Legacy().Sources()
	var out []string

	for _, m := range movedSettings(a.cfg.Legacy()) {
		want, err := settings.Encode(m.kind, m.value)
		if err != nil {
			continue
		}
		got, ok, err := a.settings.Get(ctx, m.key)
		if err != nil {
			continue
		}
		if !ok {
			got = defaultEncoded(m)
		}
		if got == want {
			continue
		}
		where := sources[m.file]
		if where == "" {
			where = config.SourceFile
		}
		out = append(out, fmt.Sprintf(
			"%s still sets %s, which is no longer read from there: this host is "+
				"running %s. Change it in Settings, or remove the key.",
			where, m.file, display(m.key, got)))
	}

	if ref := strings.TrimSpace(deref(a.cfg.Legacy().Tunnel.APIKeyRef)); ref != "" {
		if _, ok, _ := a.settings.Get(ctx, settings.KeyTunnelAPIKey); ok {
			out = append(out, "config.yaml still sets tunnel.api_key_ref, which is no "+
				"longer read from there: the key it pointed at was moved into the "+
				"database, encrypted, and is managed on the Settings page. Remove the key.")
		}
	}

	sort.Strings(out)
	return out
}

// defaultEncoded is what the store would answer with for a key nobody has set.
func defaultEncoded(m movedSetting) string {
	f, ok := settings.FieldFor(m.key)
	if !ok || f.Default == nil {
		return ""
	}
	encoded, err := json.Marshal(f.Default)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// display renders a stored value the way an operator would read it: without
// the JSON quoting, and with the unit for a duration.
//
// The unit matters. "running 30" against a file saying 90s reads as a
// disagreement about the number; "running 30 seconds" reads as what it is.
func display(key, stored string) string {
	var s string
	if err := json.Unmarshal([]byte(stored), &s); err == nil {
		if s == "" {
			return "nothing"
		}
		return s
	}
	if stored == "" {
		return "nothing"
	}
	if f, ok := settings.FieldFor(key); ok && f.Kind == settings.KindDuration {
		unit := f.Unit
		if unit == "" {
			unit = settings.UnitMinutes
		}
		return stored + " " + unit
	}
	return stored
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// applyLogSettings applies the stored level and format to the running logger,
// and keeps applying them as they change.
//
// Live rather than restart-on-change, because the logger is built with both
// formats already in it. Turning debug on to watch a problem and off again
// afterwards is the whole use of this setting, and a restart in the middle of
// the problem loses the thing being watched.
func (a *App) applyLogSettings(ctx context.Context, ctl *observability.LogControl) {
	if ctl == nil {
		return
	}
	apply := func(ctx context.Context) {
		ctl.Set(
			observability.ParseLevel(a.settings.FieldString(ctx, settings.KeyLoggingLevel)),
			a.settings.FieldString(ctx, settings.KeyLoggingFormat))
	}
	apply(ctx)
	a.settings.Watch(func(changed []string) {
		for _, key := range changed {
			if key == settings.KeyLoggingLevel || key == settings.KeyLoggingFormat {
				apply(context.Background())
				return
			}
		}
	})
}
