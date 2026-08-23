package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/config"
	"github.com/spoked/mcpd/internal/observability"
	"github.com/spoked/mcpd/internal/operations"
	"github.com/spoked/mcpd/internal/settings"
)

// What an operator's file looked like before any of this moved into the
// database. Every value differs from the default, so a test that finds the
// default has found a value that was lost.
func populatedLegacy(cfg *config.Config) {
	l := cfg.Legacy()
	l.Server.PublicURL = ptr("https://mcp.example.net")
	l.Server.FrontendPublicURL = ptr("https://mcpd.example.net")
	l.Server.FrontendEnabled = ptr(false)
	l.Server.TLS.Mode = ptr("self-signed")
	l.Server.ReadHeaderTimeout = ptr(11 * time.Second)
	l.Server.ReadTimeout = ptr(90 * time.Second)
	l.Server.WriteTimeout = ptr(200 * time.Second)
	l.Server.IdleTimeout = ptr(150 * time.Second)
	l.Server.ShutdownTimeout = ptr(45 * time.Second)
	l.Storage.BusyTimeout = ptr(9 * time.Second)
	l.Storage.RelaxedDurability = ptr(true)
	l.Auth.Accounts.SessionTTL = ptr(24 * time.Hour)
	l.Approval.ProposalTTL = ptr(45 * time.Minute)
	l.Approval.ApprovalTTL = ptr(20 * time.Minute)
	l.Approval.LeaseTTL = ptr(3 * time.Minute)
	l.Approval.InlineMaxRisk = ptr("high")
	l.Logging.Level = ptr("debug")
	l.Logging.Format = ptr("text")
	l.Tunnel.Enabled = ptr(true)
	l.Tunnel.TunnelID = ptr("tunnel_6a87964313a88191b1cf9d9bf28dde48")
	l.Tunnel.Principal = ptr("svc:chatgpt")
	l.Tunnel.Role = ptr("admin")
	l.Tunnel.Plugins = ptr([]string{"echo"})
	l.Tunnel.CheckForUpdates = ptr(false)
	l.Tunnel.DiagnosticsAddr = ptr("127.0.0.1:9095")
}

// upgradeConfig is a deployment whose database already exists at dbPath.
func upgradeConfig(t *testing.T, dbPath string) *config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.Storage.Path = dbPath
	cfg.SecretKeyRef = "env:MCPD_SECRET_KEY"
	cfg.Plugins = map[string]config.PluginConfig{"echo": {Enabled: true}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config invalid: %v", err)
	}
	return cfg
}

func startApp(t *testing.T, cfg *config.Config) *App {
	t.Helper()
	a, err := New(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { a.db.Close() })
	return a
}

// The live deployment's file is populated, and an upgrade that loses a value
// somebody chose is the failure that matters most here.
func TestUpgrade_KeepsEveryValueTheFileSet(t *testing.T) {
	t.Setenv("MCPD_SECRET_KEY", "test-encryption-key-at-least-32-chars-long")
	dbPath := filepath.Join(t.TempDir(), "mcpd.db")

	cfg := upgradeConfig(t, dbPath)
	populatedLegacy(cfg)
	a := startApp(t, cfg)
	ctx := context.Background()

	for _, tc := range []struct {
		key  string
		want string
	}{
		{settings.KeyServerPublicURL, `"https://mcp.example.net"`},
		{settings.KeyServerFrontendPublicURL, `"https://mcpd.example.net"`},
		{settings.KeyServerFrontendEnabled, "false"},
		{settings.KeyServerTLSMode, `"self-signed"`},
		{settings.KeyServerReadHeaderTimeout, "11"},
		{settings.KeyServerReadTimeout, "90"},
		{settings.KeyServerWriteTimeout, "200"},
		{settings.KeyServerIdleTimeout, "150"},
		{settings.KeyServerShutdownTimeout, "45"},
		{settings.KeyStorageBusyTimeout, "9"},
		{settings.KeyStorageRelaxedDurability, "true"},
		{settings.KeyAccountsSessionTTL, "24"},
		{settings.KeyApprovalProposalTTL, "45"},
		{settings.KeyApprovalApprovalTTL, "20"},
		{settings.KeyApprovalLeaseTTL, "3"},
		{settings.KeyApprovalInlineMaxRisk, `"high"`},
		{settings.KeyLoggingLevel, `"debug"`},
		{settings.KeyLoggingFormat, `"text"`},
		{settings.KeyTunnelEnabled, "true"},
		{settings.KeyTunnelID, `"tunnel_6a87964313a88191b1cf9d9bf28dde48"`},
		{settings.KeyTunnelPrincipal, `"svc:chatgpt"`},
		{settings.KeyTunnelRole, `"admin"`},
		{settings.KeyTunnelPlugins, `["echo"]`},
		{settings.KeyTunnelUpdates, "false"},
		{settings.KeyTunnelDiagnostics, `"127.0.0.1:9095"`},
	} {
		got, ok, err := a.settings.Get(ctx, tc.key)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Errorf("%s was not imported", tc.key)
			continue
		}
		if got != tc.want {
			t.Errorf("%s = %s, want %s", tc.key, got, tc.want)
		}
	}
}

// The reason for moving them is that a file change leaves no record. If the
// import did not go through settings_history, the values would have arrived
// with no actor and no before-and-after -- which is the thing being fixed.
func TestUpgrade_TheImportIsRecordedAndHappensOnce(t *testing.T) {
	t.Setenv("MCPD_SECRET_KEY", "test-encryption-key-at-least-32-chars-long")
	dbPath := filepath.Join(t.TempDir(), "mcpd.db")

	first := upgradeConfig(t, dbPath)
	populatedLegacy(first)
	a := startApp(t, first)
	ctx := context.Background()

	entries, err := a.settings.History(ctx, 500)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, e := range entries {
		seen[e.Key]++
		if e.ChangedBy != configImportActor {
			t.Errorf("%s was recorded against %q, want %q",
				e.Key, e.ChangedBy, configImportActor)
		}
	}
	if seen[settings.KeyServerPublicURL] != 1 {
		t.Fatalf("the public address has %d history entries, want exactly one",
			seen[settings.KeyServerPublicURL])
	}
	if seen[settings.KeyConfigImported] != 1 {
		t.Fatal("the import was not recorded")
	}
	a.db.Close()

	// A second start over the same database, with the same file. Nothing is
	// imported again, and nothing new is recorded.
	second := upgradeConfig(t, dbPath)
	populatedLegacy(second)
	b := startApp(t, second)

	after, err := b.settings.History(ctx, 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(entries) {
		t.Fatalf("a second start wrote %d more history entries; the import must "+
			"happen once", len(after)-len(entries))
	}
}

// A value somebody chose in the dashboard outranks one the deployment was
// started with, so the import fills gaps and never overwrites.
func TestUpgrade_DoesNotOverwriteWhatSomebodyAlreadyChose(t *testing.T) {
	t.Setenv("MCPD_SECRET_KEY", "test-encryption-key-at-least-32-chars-long")
	dbPath := filepath.Join(t.TempDir(), "mcpd.db")
	ctx := context.Background()

	// A deployment that has been through the dashboard: the address was set
	// there, and the import has not run.
	first := startApp(t, upgradeConfig(t, dbPath))
	if err := first.settings.Apply(ctx, "user:alice", []settings.Change{
		{Key: settings.KeyServerPublicURL, Value: `"https://chosen.example.net"`},
	}); err != nil {
		t.Fatal(err)
	}
	// Clear the marker the first start wrote, so the import is offered the
	// file again with a value already in place.
	if err := first.settings.Apply(ctx, "user:alice", []settings.Change{
		{Key: settings.KeyConfigImported, Delete: true},
	}); err != nil {
		t.Fatal(err)
	}
	first.db.Close()

	cfg := upgradeConfig(t, dbPath)
	populatedLegacy(cfg)
	second := startApp(t, cfg)

	got, _, err := second.settings.Get(ctx, settings.KeyServerPublicURL)
	if err != nil {
		t.Fatal(err)
	}
	if got != `"https://chosen.example.net"` {
		t.Fatalf("public address = %s; the import overwrote a value somebody chose", got)
	}
	// And the record says so rather than silently dropping it.
	raw, _, err := second.settings.Get(ctx, settings.KeyConfigImported)
	if err != nil {
		t.Fatal(err)
	}
	var record importRecord
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		t.Fatal(err)
	}
	if !contains(record.Kept, settings.KeyServerPublicURL) {
		t.Fatalf("the import record does not say the address was kept: %+v", record)
	}
}

// Silent disagreement between two sources of truth is the failure mode this
// design exists to remove, so a file that still names a moved key has to be
// named back.
func TestStaleFileKeysAreIgnoredAndNamed(t *testing.T) {
	t.Setenv("MCPD_SECRET_KEY", "test-encryption-key-at-least-32-chars-long")
	dbPath := filepath.Join(t.TempDir(), "mcpd.db")
	ctx := context.Background()

	first := upgradeConfig(t, dbPath)
	populatedLegacy(first)
	a := startApp(t, first)

	// Nothing disagrees yet: the store holds exactly what the file supplied.
	if got := a.staleConfigWarnings(ctx); len(got) > 0 {
		t.Fatalf("a file that agrees with the store must not be warned about: %v", got)
	}

	// Somebody changes the address in the dashboard. The file now says
	// something else, and mcpd is not reading it.
	if err := a.settings.Apply(ctx, "user:alice", []settings.Change{
		{Key: settings.KeyServerPublicURL, Value: `"https://moved.example.net"`},
	}); err != nil {
		t.Fatal(err)
	}

	warnings := a.staleConfigWarnings(ctx)
	var found string
	for _, w := range warnings {
		if strings.Contains(w, "server.public_url") {
			found = w
		}
	}
	if found == "" {
		t.Fatalf("a stale key must be named; warnings were %v", warnings)
	}
	if !strings.Contains(found, "https://moved.example.net") {
		t.Fatalf("the warning must say what the host is actually running: %q", found)
	}
	if !strings.Contains(found, config.SourceFile) {
		t.Fatalf("the warning must say where the ignored value is written: %q", found)
	}

	// And the value the file names is genuinely not in use.
	if got := a.publicURL(ctx); got != "https://moved.example.net" {
		t.Fatalf("public address = %q; the file won over the database", got)
	}
}

// An environment override is named by the variable that sets it, not by a file
// that does not mention it. Pointing an operator at config.yaml for a value
// their compose file supplies is a wasted hour.
//
// Loaded from a real file with a real variable set, because what records the
// source is the loader and this is the thing being checked.
func TestStaleEnvironmentOverridesNameTheVariable(t *testing.T) {
	t.Setenv("MCPD_SECRET_KEY", "test-encryption-key-at-least-32-chars-long")
	t.Setenv("MCPD_PUBLIC_URL", "http://from-compose.example.net")
	dir := t.TempDir()
	ctx := context.Background()

	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(
		"server:\n  listen: \"127.0.0.1:19080\"\n  frontend_listen: \"127.0.0.1:19090\"\n"+
			"storage:\n  path: "+filepath.Join(dir, "mcpd.db")+"\n"+
			"secret_key_ref: env:MCPD_SECRET_KEY\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	a := startApp(t, cfg)
	// The environment seeded the store on this first start, so nothing
	// disagrees yet.
	if got, _, _ := a.settings.Get(ctx, settings.KeyServerPublicURL); got != `"http://from-compose.example.net"` {
		t.Fatalf("the environment did not seed the store: %s", got)
	}
	if got := a.staleConfigWarnings(ctx); len(got) > 0 {
		t.Fatalf("an environment that agrees with the store must be quiet: %v", got)
	}

	if err := a.settings.Apply(ctx, "user:alice", []settings.Change{
		{Key: settings.KeyServerPublicURL, Value: `"https://chosen.example.net"`},
	}); err != nil {
		t.Fatal(err)
	}

	warnings := a.staleConfigWarnings(ctx)
	if len(warnings) == 0 || !strings.Contains(warnings[0], "MCPD_PUBLIC_URL") {
		t.Fatalf("the warning must name the variable that sets it: %v", warnings)
	}
}

// The four that stay in the file have to keep working from the file, and must
// not be settable through the dashboard: a bind address stored in the database
// is exactly the value that could lock an operator out of the page they would
// fix it on.
func TestTheKeysThatStayAreFileOnly(t *testing.T) {
	t.Setenv("MCPD_SECRET_KEY", "test-encryption-key-at-least-32-chars-long")
	dir := t.TempDir()

	cfg := upgradeConfig(t, filepath.Join(dir, "mcpd.db"))
	cfg.Server.Listen = "127.0.0.1:19080"
	cfg.Server.FrontendListen = "127.0.0.1:19090"
	a := startApp(t, cfg)

	if got := a.Addr(); got != "127.0.0.1:19080" {
		t.Errorf("listen = %q, want the file's value", got)
	}
	if got := a.frontend.Addr; got != "127.0.0.1:19090" {
		t.Errorf("frontend_listen = %q, want the file's value", got)
	}
	if got := a.db.Path(); got != filepath.Join(dir, "mcpd.db") {
		t.Errorf("storage.path = %q, want the file's value", got)
	}
	// The secret key is what everything else in the store is encrypted under,
	// so a store that can hold secrets is the proof it was read.
	if !a.settings.HasCipher() {
		t.Error("secret_key_ref was not read from the file")
	}

	// None of them is an editable setting, which is what stops the dashboard
	// from offering a form over them.
	for _, key := range []string{
		"server.listen", "server.frontend_listen", "storage.path", "secret_key_ref",
	} {
		if _, ok := settings.FieldFor(key); ok {
			t.Errorf("%s is declared as an editable setting; it must not be", key)
		}
		if err := settings.Validate(key, "anything"); err == nil {
			t.Errorf("%s was accepted as a setting; writing one must be refused", key)
		}
	}

	// And the dashboard says where they are instead, rather than leaving an
	// operator to conclude they do not exist.
	var named []string
	for _, b := range bootstrapSettings(cfg) {
		named = append(named, b.Key)
	}
	for _, key := range []string{
		"server.listen", "server.frontend_listen", "storage.path", "secret_key_ref",
	} {
		if !contains(named, key) {
			t.Errorf("%s is not shown anywhere in the dashboard", key)
		}
	}
	if len(named) != 4 {
		t.Errorf("the dashboard names %d startup-file values, want the four that "+
			"cannot move: %v", len(named), named)
	}
}

// A moved value changed in the dashboard has to reach the thing that uses it,
// or the form is reporting success and doing nothing.
func TestMovedValuesTakeEffectLive(t *testing.T) {
	t.Setenv("MCPD_SECRET_KEY", "test-encryption-key-at-least-32-chars-long")
	a := startApp(t, upgradeConfig(t, filepath.Join(t.TempDir(), "mcpd.db")))
	ctx := context.Background()

	if err := a.settings.Apply(ctx, "user:alice", []settings.Change{
		{Key: settings.KeyServerPublicURL, Value: `"https://new.example.net"`},
		{Key: settings.KeyServerFrontendPublicURL, Value: `"https://dash.example.net"`},
		{Key: settings.KeyAccountsSessionTTL, Value: "36"},
		{Key: settings.KeyServerShutdownTimeout, Value: "90"},
		{Key: settings.KeyApprovalInlineMaxRisk, Value: `"low"`},
		{Key: settings.KeyApprovalProposalTTL, Value: "7"},
	}); err != nil {
		t.Fatal(err)
	}

	if got := a.publicURL(ctx); got != "https://new.example.net" {
		t.Errorf("public address = %q", got)
	}
	if got := a.frontendPublicURL(ctx); got != "https://dash.example.net" {
		t.Errorf("dashboard address = %q", got)
	}
	if got := a.sessionTTL(ctx); got != 36*time.Hour {
		t.Errorf("session ttl = %s, want 36h", got)
	}
	if got := a.shutdownTimeout(ctx); got != 90*time.Second {
		t.Errorf("shutdown timeout = %s, want 90s", got)
	}
	policy := a.approvalPolicy(ctx)
	if got := string(policy.InlineApproval.MaxRisk); got != "low" {
		t.Errorf("inline ceiling = %q", got)
	}
	if got := policy.ProposalTTL; got != 7*time.Minute {
		t.Errorf("proposal ttl = %s, want 7m", got)
	}
}

// "Nothing may be approved in the conversation" is a real setting, and the
// dropdown spells it "none" because the policy spells it as an absence. The
// translation has to survive, or picking the strictest option would loosen the
// gate to whatever an empty string means.
func TestTheStrictestInlineCeilingIsNotAnEmptyField(t *testing.T) {
	t.Setenv("MCPD_SECRET_KEY", "test-encryption-key-at-least-32-chars-long")
	a := startApp(t, upgradeConfig(t, filepath.Join(t.TempDir(), "mcpd.db")))
	ctx := context.Background()

	if err := a.settings.Apply(ctx, "user:alice", []settings.Change{
		{Key: settings.KeyApprovalInlineMaxRisk, Value: `"` + settings.RiskNone + `"`},
	}); err != nil {
		t.Fatal(err)
	}
	policy := a.approvalPolicy(ctx)
	if policy.InlineApproval.MaxRisk != "" {
		t.Fatalf("ceiling = %q, want the policy's own empty level",
			policy.InlineApproval.MaxRisk)
	}
	for _, risk := range []string{"low", "medium", "high", "critical"} {
		if policy.InlineApproval.Allows(operations.RiskLevel(risk)) {
			t.Errorf("%s may still be approved inline under the strictest setting", risk)
		}
	}
}

// A restart-only value is stored and honestly labelled: it must not pretend to
// have taken effect, and the schema is where that claim is made.
func TestRestartOnlyValuesSaySo(t *testing.T) {
	for _, key := range []string{
		settings.KeyServerTLSMode,
		settings.KeyServerFrontendEnabled,
		settings.KeyServerReadHeaderTimeout,
		settings.KeyServerReadTimeout,
		settings.KeyServerWriteTimeout,
		settings.KeyServerIdleTimeout,
		settings.KeyStorageBusyTimeout,
		settings.KeyStorageRelaxedDurability,
	} {
		f, ok := settings.FieldFor(key)
		if !ok {
			t.Errorf("%s is not a declared setting", key)
			continue
		}
		if f.Apply != settings.ApplyRestart {
			t.Errorf("%s is declared %q; it is read once at startup and must say "+
				"it needs a restart", key, f.Apply)
		}
	}
}

// And the ones that really do apply live say that instead. A field marked
// restart when it is not trains an operator to restart for nothing.
func TestLiveValuesSaySo(t *testing.T) {
	for _, key := range []string{
		settings.KeyServerPublicURL,
		settings.KeyServerFrontendPublicURL,
		settings.KeyServerShutdownTimeout,
		settings.KeyAccountsSessionTTL,
		settings.KeyApprovalInlineMaxRisk,
		settings.KeyLoggingLevel,
		settings.KeyLoggingFormat,
	} {
		f, ok := settings.FieldFor(key)
		if !ok {
			t.Errorf("%s is not a declared setting", key)
			continue
		}
		if f.Apply != settings.ApplyLive {
			t.Errorf("%s is declared %q, but it is read at the point of use", key, f.Apply)
		}
	}
}

// The pool settings are stored inside the pools they configure, so the reopen
// that resolves that has to actually happen.
func TestStorageReopensWithItsOwnStoredSettings(t *testing.T) {
	t.Setenv("MCPD_SECRET_KEY", "test-encryption-key-at-least-32-chars-long")
	dbPath := filepath.Join(t.TempDir(), "mcpd.db")
	ctx := context.Background()

	first := startApp(t, upgradeConfig(t, dbPath))
	if err := first.settings.Apply(ctx, "user:alice", []settings.Change{
		{Key: settings.KeyStorageBusyTimeout, Value: "17"},
		{Key: settings.KeyStorageRelaxedDurability, Value: "true"},
	}); err != nil {
		t.Fatal(err)
	}
	first.db.Close()

	db, err := openStorage(ctx, upgradeConfig(t, dbPath),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var busy int
	if err := db.Reader().QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busy); err != nil {
		t.Fatal(err)
	}
	if busy != 17_000 {
		t.Errorf("busy_timeout = %dms, want the stored 17s", busy)
	}
	var sync int
	if err := db.Reader().QueryRowContext(ctx, "PRAGMA synchronous").Scan(&sync); err != nil {
		t.Fatal(err)
	}
	if sync != 1 {
		t.Errorf("synchronous = %d, want NORMAL (1) from the stored setting", sync)
	}
}

// The expensive failure: an upgrade that disturbs the encryption key makes
// every stored credential unreadable, and nothing says so beyond a credential
// quietly no longer being there.
func TestUpgradeOverAPopulatedDatabaseKeepsItsSecrets(t *testing.T) {
	t.Setenv("MCPD_SECRET_KEY", "test-encryption-key-at-least-32-chars-long")
	dbPath := filepath.Join(t.TempDir(), "mcpd.db")
	ctx := context.Background()

	const apiKey = "sk-the-operators-upstream-credential"
	before := startApp(t, upgradeConfig(t, dbPath))
	if err := before.settings.Apply(ctx, "user:alice", []settings.Change{
		{Key: settings.KeyTunnelAPIKey, Value: apiKey, Secret: true},
		{Key: settings.KeyGoogleSecret, Value: "google-client-secret", Secret: true},
		{Key: settings.KeyTunnelID, Value: `"tunnel_6a87964313a88191b1cf9d9bf28dde48"`},
	}); err != nil {
		t.Fatal(err)
	}
	before.db.Close()

	// The upgrade arrives, and the file it finds is the populated one.
	cfg := upgradeConfig(t, dbPath)
	populatedLegacy(cfg)
	cfg.Legacy().Tunnel.APIKeyRef = ptr("env:MCPD_NOT_THE_STORED_ONE")
	after := startApp(t, cfg)

	got, ok, err := after.settings.Get(ctx, settings.KeyTunnelAPIKey)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got != apiKey {
		t.Fatalf("the tunnel key is %q after the upgrade, want the stored one", got)
	}
	if got, _, _ := after.settings.Get(ctx, settings.KeyGoogleSecret); got != "google-client-secret" {
		t.Fatalf("a stored provider secret did not survive the upgrade: %q", got)
	}
	// The tunnel id was chosen in the dashboard, so the file's does not
	// replace it.
	if got, _, _ := after.settings.Get(ctx,
		settings.KeyTunnelID); got != `"tunnel_6a87964313a88191b1cf9d9bf28dde48"` {
		t.Fatalf("tunnel id = %s, want the one already stored", got)
	}
}

// A value the settings schema refuses must not arrive through the import: the
// file is not a way around validation the dashboard applies.
func TestImportRefusesAValueTheSchemaWouldNotAccept(t *testing.T) {
	t.Setenv("MCPD_SECRET_KEY", "test-encryption-key-at-least-32-chars-long")
	ctx := context.Background()

	cfg := upgradeConfig(t, filepath.Join(t.TempDir(), "mcpd.db"))
	// "approver" stopped being a role when the set collapsed to two.
	cfg.Legacy().Tunnel.Role = ptr("approver")
	cfg.Legacy().Server.PublicURL = ptr("https://mcp.example.net")

	a := startApp(t, cfg)

	if _, ok, _ := a.settings.Get(ctx, settings.KeyTunnelRole); ok {
		t.Fatal("a role this host does not have was imported")
	}
	// The rest of the file still came across: one bad value must not cost the
	// others.
	if got, _, _ := a.settings.Get(ctx, settings.KeyServerPublicURL); got != `"https://mcp.example.net"` {
		t.Fatalf("public address = %s; one refused value cost the others", got)
	}

	raw, _, err := a.settings.Get(ctx, settings.KeyConfigImported)
	if err != nil {
		t.Fatal(err)
	}
	var record importRecord
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		t.Fatal(err)
	}
	if !contains(record.Refused, settings.KeyTunnelRole) {
		t.Fatalf("the record does not say the role was refused: %+v", record)
	}
}

// A fresh deployment generates a file that supplies nothing, so it imports
// nothing and runs on the schema's declared defaults.
func TestAFreshDeploymentImportsNothingAndUsesTheDeclaredDefaults(t *testing.T) {
	t.Setenv("MCPD_SECRET_KEY", "test-encryption-key-at-least-32-chars-long")
	a := startApp(t, upgradeConfig(t, filepath.Join(t.TempDir(), "mcpd.db")))
	ctx := context.Background()

	raw, ok, err := a.settings.Get(ctx, settings.KeyConfigImported)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("a fresh deployment must still record that there was nothing to import")
	}
	var record importRecord
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		t.Fatal(err)
	}
	if len(record.Imported) != 0 {
		t.Fatalf("a file that supplies nothing imported %v", record.Imported)
	}

	if got := a.sessionTTL(ctx); got != 12*time.Hour {
		t.Errorf("session ttl = %s, want the declared 12h default", got)
	}
	if got := a.shutdownTimeout(ctx); got != 30*time.Second {
		t.Errorf("shutdown timeout = %s, want the declared 30s default", got)
	}
	policy := a.approvalPolicy(ctx)
	if policy.ProposalTTL != 30*time.Minute || policy.ApprovalTTL != 15*time.Minute {
		t.Errorf("approval TTLs = %s/%s, want the declared defaults",
			policy.ProposalTTL, policy.ApprovalTTL)
	}
	if got := string(policy.InlineApproval.MaxRisk); got != "medium" {
		t.Errorf("inline ceiling = %q, want the declared medium default", got)
	}
	if got := a.frontend; got == nil {
		t.Error("the dashboard is off by default; it was on before this moved")
	}
}

// Turning debug on to watch a problem and off again afterwards is the whole
// use of the logging settings, and a restart in the middle of the problem
// loses the thing being watched. So the change has to reach the running
// logger, not just the table.
func TestLoggingSettingsReachTheRunningLogger(t *testing.T) {
	t.Setenv("MCPD_SECRET_KEY", "test-encryption-key-at-least-32-chars-long")
	ctx := context.Background()

	var out bytes.Buffer
	log, ctl := observability.NewSwitchableLogger(&out, slog.LevelInfo, "json")

	cfg := upgradeConfig(t, filepath.Join(t.TempDir(), "mcpd.db"))
	cfg.Legacy().Logging.Level = ptr("warn")
	cfg.Legacy().Logging.Format = ptr("text")

	a, err := New(ctx, cfg, log, WithLogControl(ctl))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.db.Close() })

	// The file's values were imported and applied on the way up.
	out.Reset()
	log.Info("not at warn")
	if out.Len() != 0 {
		t.Fatalf("the imported level was not applied: %s", out.String())
	}
	log.Warn("visible")
	if got := out.String(); strings.HasPrefix(got, "{") {
		t.Fatalf("the imported format was not applied: %q", got)
	}

	// And a change made afterwards reaches it without a restart.
	if err := a.settings.Apply(ctx, "user:alice", []settings.Change{
		{Key: settings.KeyLoggingLevel, Value: `"debug"`},
		{Key: settings.KeyLoggingFormat, Value: `"json"`},
	}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	log.Debug("now visible")
	got := out.String()
	if !strings.Contains(got, "now visible") {
		t.Fatalf("turning debug on in the dashboard did nothing: %q", got)
	}
	if !strings.HasPrefix(got, "{") {
		t.Fatalf("the format change did not reach the logger: %q", got)
	}
}

func contains(haystack []string, needle string) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}
