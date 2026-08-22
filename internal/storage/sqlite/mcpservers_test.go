package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/mcpservers"
)

// TestMigrate_UpgradedSchemaMatchesFresh defends the rule that makes
// forward-only migrations safe to trust: a database that was created before a
// migration existed, and then upgraded, must be indistinguishable from one
// created after it.
//
// Without this, a table added in 0009 could carry a constraint on a fresh
// install that an upgraded install silently lacks, and the difference would
// only show up as a corrupt row months later.
func TestMigrate_UpgradedSchemaMatchesFresh(t *testing.T) {
	ctx := context.Background()

	fresh := newTestDB(t)

	// An installation that stopped one migration short of the newest, which is
	// what every existing deployment looks like the moment before an upgrade.
	older, err := Open(ctx, Options{
		Path:              filepath.Join(t.TempDir(), "older.db"),
		RelaxedDurability: true,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { older.Close() })

	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	if len(migrations) < 2 {
		t.Fatalf("expected more than one migration, got %d", len(migrations))
	}
	if _, err := older.Writer().ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			name       TEXT    NOT NULL,
			checksum   TEXT    NOT NULL,
			applied_at INTEGER NOT NULL
		) STRICT`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	for _, m := range migrations[:len(migrations)-1] {
		if err := applyOne(ctx, older.Writer(), m); err != nil {
			t.Fatalf("apply %04d: %v", m.version, err)
		}
	}

	// Now upgrade it the way a restart would.
	applied, err := Migrate(ctx, older)
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if applied != 1 {
		t.Fatalf("expected exactly the newest migration to apply, got %d", applied)
	}

	if got, want := schemaOf(t, older), schemaOf(t, fresh); got != want {
		t.Errorf("an upgraded database does not match a fresh one\n--- upgraded ---\n%s\n--- fresh ---\n%s",
			got, want)
	}
}

// schemaOf reads every object SQLite knows about, in a stable order.
func schemaOf(t *testing.T, db *DB) string {
	t.Helper()
	rows, err := db.Reader().QueryContext(context.Background(), `
		SELECT type, name, COALESCE(sql,'')
		  FROM sqlite_master
		 WHERE name NOT LIKE 'sqlite_%'
		 ORDER BY type, name`)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	defer rows.Close()

	var b strings.Builder
	for rows.Next() {
		var kind, name, sql string
		if err := rows.Scan(&kind, &name, &sql); err != nil {
			t.Fatalf("scan: %v", err)
		}
		fmt.Fprintf(&b, "%s %s\n%s\n\n", kind, name, sql)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return b.String()
}

func newMCPStore(t *testing.T) (*MCPServerStore, *DB) {
	t.Helper()
	db := newTestDB(t)
	return NewMCPServerStore(db, func() time.Time { return testClock }), db
}

func tool(name, description string) mcpservers.Tool {
	d := mcpservers.Descriptor{
		Name:        name,
		Description: description,
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}
	hash, err := mcpservers.HashDescriptor(d)
	if err != nil {
		panic(err)
	}
	return mcpservers.Tool{Name: name, Descriptor: d, Hash: hash}
}

func importFixture(t *testing.T, s *MCPServerStore) {
	t.Helper()
	doc := []byte(`{"$schema":"` + mcpservers.SchemaURI + `","name":"io.example/x",` +
		`"description":"x","version":"1.0.0","remotes":[{"type":"streamable-http",` +
		`"url":"https://example.test/mcp"}]}`)
	if err := s.Import(context.Background(), "weather", doc,
		mcpservers.SchemaURI, mcpservers.TransportStreamableHTTP, "https://example.test/mcp"); err != nil {
		t.Fatalf("import: %v", err)
	}
}

func TestImport_RefusesADuplicateName(t *testing.T) {
	s, _ := newMCPStore(t)
	importFixture(t, s)

	err := s.Import(context.Background(), "weather", []byte(`{}`), "v", "streamable-http", "https://x.test/mcp")
	if !errors.Is(err, ErrServerExists) {
		t.Fatalf("expected ErrServerExists, got %v", err)
	}
}

// TestSnapshot_NewToolArrivesPending is the rule that keeps a remote server
// from putting something in front of a model on its own schedule.
func TestSnapshot_NewToolArrivesPending(t *testing.T) {
	ctx := context.Background()
	s, _ := newMCPStore(t)
	importFixture(t, s)

	if _, err := s.Snapshot(ctx, "weather", []mcpservers.Tool{tool("forecast", "a")}); err != nil {
		t.Fatalf("first discovery: %v", err)
	}
	enable(t, s, "weather", "forecast")

	diff, err := s.Snapshot(ctx, "weather", []mcpservers.Tool{
		tool("forecast", "a"), tool("alerts", "b"),
	})
	if err != nil {
		t.Fatalf("second discovery: %v", err)
	}
	if len(diff.Added) != 1 || diff.Added[0] != "alerts" {
		t.Fatalf("expected alerts to be reported as added, got %+v", diff)
	}

	states := statesOf(t, s, "weather")
	if states["alerts"] != mcpservers.ToolPending {
		t.Errorf("a newly discovered tool must arrive pending, got %q", states["alerts"])
	}
	if states["forecast"] != mcpservers.ToolEnabled {
		t.Errorf("an unchanged approved tool must stay enabled, got %q", states["forecast"])
	}

	mounted, err := s.EnabledTools(ctx, "weather")
	if err != nil {
		t.Fatalf("enabled tools: %v", err)
	}
	if len(mounted) != 1 || mounted[0].Name != "forecast" {
		t.Errorf("only the approved tool should mount, got %+v", names(mounted))
	}
}

// TestSnapshot_WithdrawnToolIsDisabledAndKept defends two things at once: the
// tool stops being served, and the row survives so its return is recognised.
func TestSnapshot_WithdrawnToolIsDisabledAndKept(t *testing.T) {
	ctx := context.Background()
	s, _ := newMCPStore(t)
	importFixture(t, s)

	if _, err := s.Snapshot(ctx, "weather", []mcpservers.Tool{
		tool("forecast", "a"), tool("alerts", "b"),
	}); err != nil {
		t.Fatalf("first discovery: %v", err)
	}
	enable(t, s, "weather", "forecast")
	enable(t, s, "weather", "alerts")

	diff, err := s.Snapshot(ctx, "weather", []mcpservers.Tool{tool("forecast", "a")})
	if err != nil {
		t.Fatalf("second discovery: %v", err)
	}
	if len(diff.Removed) != 1 || diff.Removed[0] != "alerts" {
		t.Fatalf("expected alerts to be reported as removed, got %+v", diff)
	}

	all, err := s.Tools(ctx, "weather")
	if err != nil {
		t.Fatalf("tools: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("a withdrawn tool must be kept, not deleted; got %+v", names(all))
	}
	if statesOf(t, s, "weather")["alerts"] != mcpservers.ToolDisabled {
		t.Errorf("a withdrawn tool must be disabled")
	}

	// It comes back: still disabled, because nobody has looked at it again.
	if _, err := s.Snapshot(ctx, "weather", []mcpservers.Tool{
		tool("forecast", "a"), tool("alerts", "b"),
	}); err != nil {
		t.Fatalf("third discovery: %v", err)
	}
	if got := statesOf(t, s, "weather")["alerts"]; got != mcpservers.ToolDisabled {
		t.Errorf("a tool that reappears unchanged must stay disabled, got %q", got)
	}
}

// TestSnapshot_ChangedDescriptorLosesItsApproval is the bug this design exists
// for: an administrator approves a description and a schema, and a server that
// swaps them afterwards must not inherit the approval.
func TestSnapshot_ChangedDescriptorLosesItsApproval(t *testing.T) {
	ctx := context.Background()
	s, _ := newMCPStore(t)
	importFixture(t, s)

	if _, err := s.Snapshot(ctx, "weather", []mcpservers.Tool{tool("forecast", "reads the forecast")}); err != nil {
		t.Fatalf("first discovery: %v", err)
	}
	enable(t, s, "weather", "forecast")

	diff, err := s.Snapshot(ctx, "weather", []mcpservers.Tool{
		tool("forecast", "reads the forecast and emails your contacts"),
	})
	if err != nil {
		t.Fatalf("second discovery: %v", err)
	}
	if len(diff.Changed) != 1 {
		t.Fatalf("expected the changed tool to be reported, got %+v", diff)
	}
	if got := statesOf(t, s, "weather")["forecast"]; got != mcpservers.ToolPending {
		t.Errorf("a tool whose descriptor changed must return to pending, got %q", got)
	}
}

// TestClassifyTool_IsGuardedByTheDescriptorHash checks that the guard is in the
// statement rather than in a read beforehand.
func TestClassifyTool_IsGuardedByTheDescriptorHash(t *testing.T) {
	ctx := context.Background()
	s, _ := newMCPStore(t)
	importFixture(t, s)

	original := tool("forecast", "reads the forecast")
	if _, err := s.Snapshot(ctx, "weather", []mcpservers.Tool{original}); err != nil {
		t.Fatalf("discovery: %v", err)
	}

	// The server changes the tool while an administrator is reading it.
	if _, err := s.Snapshot(ctx, "weather", []mcpservers.Tool{tool("forecast", "something else")}); err != nil {
		t.Fatalf("second discovery: %v", err)
	}

	err := s.ClassifyTool(ctx, "weather", "forecast", original.Hash, mcpservers.ToolEnabled)
	if !errors.Is(err, ErrToolClassification) {
		t.Fatalf("approving a descriptor that has since changed must be refused, got %v", err)
	}
	if got := statesOf(t, s, "weather")["forecast"]; got != mcpservers.ToolPending {
		t.Errorf("state should be untouched, got %q", got)
	}
}

// TestClassifyTool_RefusesToEnableAnUnusableTool checks the other half of the
// guard: a tool this host recorded a problem against cannot be marked served.
func TestClassifyTool_RefusesToEnableAnUnusableTool(t *testing.T) {
	ctx := context.Background()
	s, _ := newMCPStore(t)
	importFixture(t, s)

	bad := tool("forecast", "a")
	bad.Problem = "the tool publishes no input schema"

	if _, err := s.Snapshot(ctx, "weather", []mcpservers.Tool{bad}); err != nil {
		t.Fatalf("discovery: %v", err)
	}
	err := s.ClassifyTool(ctx, "weather", "forecast", bad.Hash, mcpservers.ToolEnabled)
	if !errors.Is(err, ErrToolClassification) {
		t.Fatalf("expected the enable to be refused, got %v", err)
	}
	// Disabling one always works: there is no reason to make an operator fight
	// to turn something off.
	if err := s.ClassifyTool(ctx, "weather", "forecast", bad.Hash, mcpservers.ToolDisabled); err != nil {
		t.Fatalf("disabling should be allowed: %v", err)
	}
}

// TestSnapshot_DemotesAnEnabledToolThatBecameUnusable covers the case where a
// server replaces a good schema with a bad one under an approval.
func TestSnapshot_DemotesAnEnabledToolThatBecameUnusable(t *testing.T) {
	ctx := context.Background()
	s, _ := newMCPStore(t)
	importFixture(t, s)

	if _, err := s.Snapshot(ctx, "weather", []mcpservers.Tool{tool("forecast", "a")}); err != nil {
		t.Fatalf("discovery: %v", err)
	}
	enable(t, s, "weather", "forecast")

	broken := tool("forecast", "a")
	broken.Descriptor.InputSchema = json.RawMessage(`{"type":"string"}`)
	broken.Hash, _ = mcpservers.HashDescriptor(broken.Descriptor)
	broken.Problem = "the tool's input schema declares type \"string\""

	if _, err := s.Snapshot(ctx, "weather", []mcpservers.Tool{broken}); err != nil {
		t.Fatalf("second discovery: %v", err)
	}
	if got := statesOf(t, s, "weather")["forecast"]; got != mcpservers.ToolDisabled {
		t.Errorf("a tool that became unusable must be disabled, got %q", got)
	}
}

func TestRemove_TakesTheToolSnapshotWithIt(t *testing.T) {
	ctx := context.Background()
	s, _ := newMCPStore(t)
	importFixture(t, s)
	if _, err := s.Snapshot(ctx, "weather", []mcpservers.Tool{tool("forecast", "a")}); err != nil {
		t.Fatalf("discovery: %v", err)
	}

	if err := s.Remove(ctx, "weather"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	tools, err := s.Tools(ctx, "weather")
	if err != nil {
		t.Fatalf("tools: %v", err)
	}
	if len(tools) != 0 {
		t.Errorf("a removed server must not leave approvals behind, got %+v", names(tools))
	}
}

func enable(t *testing.T, s *MCPServerStore, server, name string) {
	t.Helper()
	tools, err := s.Tools(context.Background(), server)
	if err != nil {
		t.Fatalf("tools: %v", err)
	}
	for _, tl := range tools {
		if tl.Name != name {
			continue
		}
		if err := s.ClassifyTool(context.Background(), server, name, tl.Hash, mcpservers.ToolEnabled); err != nil {
			t.Fatalf("enable %s: %v", name, err)
		}
		return
	}
	t.Fatalf("no tool named %q", name)
}

func statesOf(t *testing.T, s *MCPServerStore, server string) map[string]mcpservers.ToolState {
	t.Helper()
	tools, err := s.Tools(context.Background(), server)
	if err != nil {
		t.Fatalf("tools: %v", err)
	}
	out := map[string]mcpservers.ToolState{}
	for _, tl := range tools {
		out[tl.Name] = tl.State
	}
	return out
}

func names(tools []mcpservers.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	return out
}
