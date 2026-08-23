package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/config"
	"github.com/spoked/mcpd/internal/settings"
	"github.com/spoked/mcpd/internal/storage/sqlite"
)

// clearEnv unsets a variable for the duration of a test and restores whatever
// was there before. t.Setenv is what records the original, so this leaves the
// developer's own environment alone even though the code under test calls
// os.Setenv itself.
func clearEnv(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		t.Setenv(n, "placeholder")
		if err := os.Unsetenv(n); err != nil {
			t.Fatal(err)
		}
	}
}

var generatedSecrets = []string{"MCPD_SECRET_KEY"}

func TestInitializeGeneratesAConfigAndSecrets(t *testing.T) {
	clearEnv(t, append(append(generatedSecrets, "MCPD_TOKEN_LOCAL"), "MCPD_LISTEN", "MCPD_FRONTEND_LISTEN", "MCPD_PUBLIC_URL")...)
	dir := t.TempDir()

	if err := initialize(dir); err != nil {
		t.Fatal(err)
	}

	env := readEnvFile(t, filepath.Join(dir, ".env"))
	for _, name := range generatedSecrets {
		if len(env[name]) < 32 {
			t.Fatalf("%s = %q, want a generated secret", name, env[name])
		}
	}
	// No bearer token is generated any more: machine callers use a key issued
	// from the dashboard, which can be revoked without a restart and says in
	// the audit trail which one acted. A generated one would be a credential
	// nobody asked for, sitting on disk with nothing recording its use.
	if _, ok := env["MCPD_TOKEN_LOCAL"]; ok {
		t.Fatal("a generated deployment must not ship a bearer token")
	}

	info, err := os.Stat(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf(".env mode = %04o, want 0600: it holds the encryption key", perm)
	}

	if _, err := os.Stat(filepath.Join(dir, "plugins")); err != nil {
		t.Fatalf("plugins directory: %v", err)
	}
}

// The refusal is the guard on the whole design: a second -init that replaced
// the encryption key would leave every stored credential undecryptable.
func TestInitializeRefusesToOverwrite(t *testing.T) {
	clearEnv(t, generatedSecrets...)
	dir := t.TempDir()

	if err := initialize(dir); err != nil {
		t.Fatal(err)
	}
	before := readEnvFile(t, filepath.Join(dir, ".env"))

	if err := initialize(dir); err == nil {
		t.Fatal("a second -init on a live deployment must fail")
	}

	after := readEnvFile(t, filepath.Join(dir, ".env"))
	if after["MCPD_SECRET_KEY"] != before["MCPD_SECRET_KEY"] {
		t.Fatal("the encryption key changed under a refused -init")
	}
}

// A key the environment already supplies is this deployment's key. Writing a
// second, different one into .env would be a decoy that took over silently the
// day the environment stopped supplying one.
func TestInitializeDoesNotCompeteWithAnEnvironmentSecret(t *testing.T) {
	clearEnv(t, generatedSecrets...)
	const existing = "an-existing-key-from-the-operators-env"
	t.Setenv("MCPD_SECRET_KEY", existing)

	dir := t.TempDir()
	if err := initialize(dir); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), existing) {
		t.Fatal("the environment's key was copied into .env; it must stay in one place")
	}
	if env := readEnvFile(t, filepath.Join(dir, ".env")); env["MCPD_SECRET_KEY"] != "" {
		t.Fatalf("MCPD_SECRET_KEY = %q, want no assignment at all", env["MCPD_SECRET_KEY"])
	}
	if env := readEnvFile(t, filepath.Join(dir, ".env")); env["MCPD_TOKEN_LOCAL"] != "" {
		t.Fatal("no bearer token is generated, supplied or not")
	}
}

// The container generates its config on first start and publishes 8080/8081,
// so a generated file binding the source defaults would leave the dashboard on
// a port nothing is mapped to.
func TestInitializeTakesAddressesFromTheEnvironment(t *testing.T) {
	clearEnv(t, generatedSecrets...)
	t.Setenv("MCPD_LISTEN", ":8080")
	t.Setenv("MCPD_FRONTEND_LISTEN", ":8081")

	dir := t.TempDir()
	if err := initialize(dir); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`listen: ":8080"`, `frontend_listen: ":8081"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("generated config is missing %q", want)
		}
	}
}

// The generated file is the minimum, and the minimum is a claim worth
// defending: it is the whole argument for having moved everything else.
//
// Four values, and every one of them is here because it cannot be in the
// database. Anything else appearing in this file is a key that could have been
// a setting -- recorded, attributed, and changeable without an editor -- and
// was not.
func TestTheGeneratedConfigIsTheMinimum(t *testing.T) {
	clearEnv(t, generatedSecrets...)
	dir := t.TempDir()
	if err := initialize(dir); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	var lines []string
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		lines = append(lines, trimmed)
	}

	want := []string{
		"server:",
		`listen: "127.0.0.1:9080"`,
		`frontend_listen: "127.0.0.1:9090"`,
		"storage:",
		"path: " + filepath.Join(dir, "mcpd.db"),
		"secret_key_ref: env:MCPD_SECRET_KEY",
	}
	if len(lines) != len(want) {
		t.Fatalf("the generated file has %d lines of configuration, want %d:\n%s",
			len(lines), len(want), strings.Join(lines, "\n"))
	}
	for i, line := range lines {
		if line != want[i] {
			t.Errorf("line %d = %q, want %q", i+1, line, want[i])
		}
	}
}

// A generated deployment has to start, which means the file it generates has
// to pass the validation the binary applies to it.
func TestTheGeneratedConfigValidates(t *testing.T) {
	clearEnv(t, generatedSecrets...)
	dir := t.TempDir()
	if err := initialize(dir); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("the config mcpd generates does not load: %v", err)
	}
	// And it supplies nothing that has moved, so a fresh deployment imports
	// nothing and warns about nothing.
	if cfg.Legacy().Any() {
		t.Fatalf("the generated file still sets keys that live in the database: %v",
			cfg.Legacy().Sources())
	}
}

// The bug this defends against is the expensive one: a restart that generates
// a new encryption key makes every credential saved through the dashboard
// undecryptable, and nothing says so until somebody opens a plugin.
//
// So this runs the real sequence -- generate, start, save a secret, stop,
// start again with a process environment as empty as a fresh container's, and
// read the secret back.
func TestSavedSecretsSurviveARestart(t *testing.T) {
	clearEnv(t, generatedSecrets...)
	dir := t.TempDir()

	if err := initialize(dir); err != nil {
		t.Fatal(err)
	}
	generated := readEnvFile(t, filepath.Join(dir, ".env"))["MCPD_SECRET_KEY"]

	const apiKey = "sk-the-operators-upstream-credential"
	firstStart := startLikeMain(t, dir)
	if err := firstStart.SetSecret(context.Background(), "user:alice", settings.KeyTunnelAPIKey, apiKey); err != nil {
		t.Fatal(err)
	}

	// A fresh process holds none of the first one's environment. Everything
	// the second start needs has to come off disk.
	clearEnv(t, generatedSecrets...)

	secondStart := startLikeMain(t, dir)
	got, found, err := secondStart.Get(context.Background(), settings.KeyTunnelAPIKey)
	if err != nil {
		t.Fatalf("reading the secret back after a restart: %v", err)
	}
	if !found {
		t.Fatal("the secret is gone after a restart")
	}
	if got != apiKey {
		t.Fatalf("secret = %q, want %q", got, apiKey)
	}

	if now := readEnvFile(t, filepath.Join(dir, ".env"))["MCPD_SECRET_KEY"]; now != generated {
		t.Fatal("the encryption key was regenerated across a restart")
	}
}

// The companion to the test above, and what gives it teeth: it shows that
// reading the secret back is a real check on the key rather than something
// that would pass whatever .env held.
func TestARegeneratedKeyLosesStoredSecrets(t *testing.T) {
	clearEnv(t, generatedSecrets...)
	dir := t.TempDir()

	if err := initialize(dir); err != nil {
		t.Fatal(err)
	}
	store := startLikeMain(t, dir)
	if err := store.SetSecret(context.Background(), "user:alice", settings.KeyTunnelAPIKey, "sk-lost"); err != nil {
		t.Fatal(err)
	}

	// What a regenerating container would do: a different key on the next
	// start, with the database untouched.
	clearEnv(t, generatedSecrets...)
	replaceKey(t, filepath.Join(dir, ".env"), "a-completely-different-encryption-key")

	// Get drops what it cannot decrypt rather than failing the whole read, so
	// a lost key shows up as a credential that is simply no longer there --
	// which is exactly why the restart test asserting `found` has teeth.
	_, found, err := startLikeMain(t, dir).Get(context.Background(), settings.KeyTunnelAPIKey)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("a secret decrypted under a key it was not written with")
	}
}

func replaceKey(t *testing.T, path, key string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "MCPD_SECRET_KEY=") {
			lines[i] = "MCPD_SECRET_KEY=" + key
		}
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
}

// startLikeMain performs the part of run() that decides which key encrypts
// stored credentials: read the .env beside the config, load the config,
// resolve secret_key_ref, open the database.
func startLikeMain(t *testing.T, dir string) *settings.Store {
	t.Helper()
	ctx := context.Background()

	if err := config.LoadEnvFile(filepath.Join(dir, config.DefaultEnvFile)); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	key, err := settings.ResolveKey(cfg.SecretKeyRef, "")
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := settings.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}

	db, err := sqlite.Open(ctx, sqlite.Options{Path: cfg.Storage.Path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := sqlite.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	return settings.NewStore(db, cipher, time.Now)
}

// readEnvFile parses assignments out of a generated .env without touching the
// process environment, so a test can assert on what was written rather than on
// what happened to be exported.
func readEnvFile(t *testing.T, path string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}
