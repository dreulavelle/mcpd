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
	"github.com/spoked/mcpd/internal/operations"
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
	if err := s.Import(context.Background(), "admin@example.test", "weather", doc,
		mcpservers.SchemaURI, mcpservers.TransportStreamableHTTP, "https://example.test/mcp"); err != nil {
		t.Fatalf("import: %v", err)
	}
}

func TestImport_RefusesADuplicateName(t *testing.T) {
	s, _ := newMCPStore(t)
	importFixture(t, s)

	err := s.Import(context.Background(), "admin@example.test", "weather", []byte(`{}`), "v", "streamable-http", "https://x.test/mcp")
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

	if _, err := s.Snapshot(ctx, "admin@example.test", "weather", []mcpservers.Tool{tool("forecast", "a")}); err != nil {
		t.Fatalf("first discovery: %v", err)
	}
	enable(t, s, "weather", "forecast")

	diff, err := s.Snapshot(ctx, "admin@example.test", "weather", []mcpservers.Tool{
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

	if _, err := s.Snapshot(ctx, "admin@example.test", "weather", []mcpservers.Tool{
		tool("forecast", "a"), tool("alerts", "b"),
	}); err != nil {
		t.Fatalf("first discovery: %v", err)
	}
	enable(t, s, "weather", "forecast")
	enable(t, s, "weather", "alerts")

	diff, err := s.Snapshot(ctx, "admin@example.test", "weather", []mcpservers.Tool{tool("forecast", "a")})
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
	if _, err := s.Snapshot(ctx, "admin@example.test", "weather", []mcpservers.Tool{
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

	if _, err := s.Snapshot(ctx, "admin@example.test", "weather", []mcpservers.Tool{tool("forecast", "reads the forecast")}); err != nil {
		t.Fatalf("first discovery: %v", err)
	}
	enable(t, s, "weather", "forecast")

	diff, err := s.Snapshot(ctx, "admin@example.test", "weather", []mcpservers.Tool{
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
	if _, err := s.Snapshot(ctx, "admin@example.test", "weather", []mcpservers.Tool{original}); err != nil {
		t.Fatalf("discovery: %v", err)
	}

	// The server changes the tool while an administrator is reading it.
	if _, err := s.Snapshot(ctx, "admin@example.test", "weather", []mcpservers.Tool{tool("forecast", "something else")}); err != nil {
		t.Fatalf("second discovery: %v", err)
	}

	err := s.ClassifyTool(ctx, "admin@example.test", "weather", "forecast", original.Hash, mcpservers.ToolEnabled)
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

	if _, err := s.Snapshot(ctx, "admin@example.test", "weather", []mcpservers.Tool{bad}); err != nil {
		t.Fatalf("discovery: %v", err)
	}
	err := s.ClassifyTool(ctx, "admin@example.test", "weather", "forecast", bad.Hash, mcpservers.ToolEnabled)
	if !errors.Is(err, ErrToolClassification) {
		t.Fatalf("expected the enable to be refused, got %v", err)
	}
	// Disabling one always works: there is no reason to make an operator fight
	// to turn something off.
	if err := s.ClassifyTool(ctx, "admin@example.test", "weather", "forecast", bad.Hash, mcpservers.ToolDisabled); err != nil {
		t.Fatalf("disabling should be allowed: %v", err)
	}
}

// TestSnapshot_DemotesAnEnabledToolThatBecameUnusable covers the case where a
// server replaces a good schema with a bad one under an approval.
func TestSnapshot_DemotesAnEnabledToolThatBecameUnusable(t *testing.T) {
	ctx := context.Background()
	s, _ := newMCPStore(t)
	importFixture(t, s)

	if _, err := s.Snapshot(ctx, "admin@example.test", "weather", []mcpservers.Tool{tool("forecast", "a")}); err != nil {
		t.Fatalf("discovery: %v", err)
	}
	enable(t, s, "weather", "forecast")

	broken := tool("forecast", "a")
	broken.Descriptor.InputSchema = json.RawMessage(`{"type":"string"}`)
	broken.Hash, _ = mcpservers.HashDescriptor(broken.Descriptor)
	broken.Problem = "the tool's input schema declares type \"string\""

	if _, err := s.Snapshot(ctx, "admin@example.test", "weather", []mcpservers.Tool{broken}); err != nil {
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
	if _, err := s.Snapshot(ctx, "admin@example.test", "weather", []mcpservers.Tool{tool("forecast", "a")}); err != nil {
		t.Fatalf("discovery: %v", err)
	}

	if err := s.Remove(ctx, "admin@example.test", "weather"); err != nil {
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
		if err := s.ClassifyTool(context.Background(), "admin@example.test", server, name, tl.Hash, mcpservers.ToolEnabled); err != nil {
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

// TestSnapshot_PrunesWithdrawnToolsNobodyClassified is C11.
//
// Keeping a withdrawn tool is right when somebody made a decision about it: a
// refusal is a record worth having, and a return is worth recognising as a
// return. It is wrong for a tool that came and went before anyone looked --
// there is no decision to preserve, and a server rotating unique names on
// every discovery would grow this table until reading it materialises the
// whole history.
func TestSnapshot_PrunesWithdrawnToolsNobodyClassified(t *testing.T) {
	ctx := context.Background()
	s, _ := newMCPStore(t)
	importFixture(t, s)

	if _, err := s.Snapshot(ctx, "admin@example.test", "weather", []mcpservers.Tool{
		tool("kept_enabled", "a"),
		tool("kept_refused", "b"),
		tool("pruned_pending", "c"),
	}); err != nil {
		t.Fatalf("first discovery: %v", err)
	}

	enable(t, s, "weather", "kept_enabled")
	// A refusal is a classification too.
	refuse(t, s, "weather", "kept_refused")
	// pruned_pending is left as nobody looked at it.

	// The server withdraws all three.
	diff, err := s.Snapshot(ctx, "admin@example.test", "weather", []mcpservers.Tool{tool("something_else", "d")})
	if err != nil {
		t.Fatalf("second discovery: %v", err)
	}
	// kept_refused is not reported: it was already disabled, so it was not
	// being served and the server dropping it is not news.
	if strings.Join(diff.Removed, ",") != "kept_enabled,pruned_pending" {
		t.Fatalf("withdrawn = %v, want the two that were still live", diff.Removed)
	}

	states := statesOf(t, s, "weather")
	for _, name := range []string{"kept_enabled", "kept_refused"} {
		if _, ok := states[name]; !ok {
			t.Errorf("%s was classified by a person and must be kept", name)
		} else if states[name] != mcpservers.ToolDisabled {
			t.Errorf("%s = %q, want disabled", name, states[name])
		}
	}
	if _, ok := states["pruned_pending"]; ok {
		t.Error("a withdrawn tool nobody classified should not be kept")
	}
	if states["something_else"] != mcpservers.ToolPending {
		t.Errorf("the new tool should be pending, got %q", states["something_else"])
	}
}

// TestSnapshot_RotatingToolNamesDoNotAccumulate is the shape the prune exists
// for: a server that offers a fresh set of names on every discovery.
func TestSnapshot_RotatingToolNamesDoNotAccumulate(t *testing.T) {
	ctx := context.Background()
	s, _ := newMCPStore(t)
	importFixture(t, s)

	for round := range 20 {
		batch := make([]mcpservers.Tool, 0, 5)
		for i := range 5 {
			batch = append(batch, tool(fmt.Sprintf("round%d_tool%d", round, i), "x"))
		}
		if _, err := s.Snapshot(ctx, "admin@example.test", "weather", batch); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
	}

	stored, err := s.Tools(ctx, "weather")
	if err != nil {
		t.Fatalf("tools: %v", err)
	}
	if len(stored) != 5 {
		t.Errorf("after 20 rounds of 5 rotating names the table holds %d rows; "+
			"only the current round should remain", len(stored))
	}
}

// A classified tool that comes back is still recognised as a return, which is
// the property the prune must not cost.
func TestSnapshot_AClassifiedToolThatReturnsIsStillKnown(t *testing.T) {
	ctx := context.Background()
	s, _ := newMCPStore(t)
	importFixture(t, s)

	if _, err := s.Snapshot(ctx, "admin@example.test", "weather", []mcpservers.Tool{tool("forecast", "a")}); err != nil {
		t.Fatalf("first discovery: %v", err)
	}
	refuse(t, s, "weather", "forecast")

	if _, err := s.Snapshot(ctx, "admin@example.test", "weather", []mcpservers.Tool{}); err != nil {
		t.Fatalf("withdrawal: %v", err)
	}
	if _, err := s.Snapshot(ctx, "admin@example.test", "weather", []mcpservers.Tool{tool("forecast", "a")}); err != nil {
		t.Fatalf("return: %v", err)
	}

	if got := statesOf(t, s, "weather")["forecast"]; got != mcpservers.ToolDisabled {
		t.Errorf("a refused tool that reappears must still be refused, got %q", got)
	}
}

func refuse(t *testing.T, s *MCPServerStore, server, name string) {
	t.Helper()
	tools, err := s.Tools(context.Background(), server)
	if err != nil {
		t.Fatalf("tools: %v", err)
	}
	for _, tl := range tools {
		if tl.Name != name {
			continue
		}
		if err := s.ClassifyTool(context.Background(), "admin@example.test", server, name, tl.Hash,
			mcpservers.ToolDisabled); err != nil {
			t.Fatalf("refuse %s: %v", name, err)
		}
		return
	}
	t.Fatalf("no tool named %q", name)
}

// Every administrative act against a remote server left no trace at all:
// importing one, running discovery, enabling a tool and removing the server
// produced zero audit rows and zero settings_history rows. Enabling a tool is
// a privilege grant -- it hands every caller of that plugin a path into a
// third party's code -- and it happened with nothing recording who did it.
//
// The entries go into the operations audit chain, which is hash-linked and
// append-only, and which already carries entries belonging to no operation.
func TestMCPServerStore_AdminActionsAreAudited(t *testing.T) {
	ctx := context.Background()
	s, db := newMCPStore(t)
	const actor = "user:admin@example.test"

	doc := []byte(`{"$schema":"` + mcpservers.SchemaURI + `","name":"io.example/x",` +
		`"description":"x","version":"1.0.0","remotes":[{"type":"streamable-http",` +
		`"url":"https://example.test/mcp"}]}`)
	if err := s.Import(ctx, actor, "weather", doc, mcpservers.SchemaURI,
		mcpservers.TransportStreamableHTTP, "https://example.test/mcp"); err != nil {
		t.Fatalf("import: %v", err)
	}
	if _, err := s.Snapshot(ctx, actor, "weather", []mcpservers.Tool{tool("forecast", "a")}); err != nil {
		t.Fatalf("discover: %v", err)
	}
	tools, err := s.Tools(ctx, "weather")
	if err != nil {
		t.Fatalf("tools: %v", err)
	}
	if err := s.ClassifyTool(ctx, actor, "weather", "forecast", tools[0].Hash,
		mcpservers.ToolEnabled); err != nil {
		t.Fatalf("classify: %v", err)
	}
	if err := s.SetEnabled(ctx, actor, "weather", false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if err := s.Remove(ctx, actor, "weather"); err != nil {
		t.Fatalf("remove: %v", err)
	}

	audit := NewAuditStore(db)
	records, err := audit.Recent(ctx, 100)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}

	byKind := map[string]operations.AuditRecord{}
	for _, r := range records {
		byKind[r.Entry.Kind] = r
	}
	for _, kind := range []string{
		"mcpserver.imported", "mcpserver.discovered", "mcpserver.tool_classified",
		"mcpserver.disabled", "mcpserver.removed",
	} {
		rec, ok := byKind[kind]
		if !ok {
			t.Errorf("no %s audit entry; the acting principal is unrecorded", kind)
			continue
		}
		if rec.Entry.Actor != actor {
			t.Errorf("%s recorded actor %q, want %q", kind, rec.Entry.Actor, actor)
		}
		if rec.Entry.Plugin != "weather" {
			t.Errorf("%s recorded plugin %q, want the server's instance name",
				kind, rec.Entry.Plugin)
		}
	}

	// The grant says what was granted, not merely that something was.
	var classified map[string]any
	if err := json.Unmarshal(byKind["mcpserver.tool_classified"].Entry.Detail, &classified); err != nil {
		t.Fatalf("decode classification detail: %v", err)
	}
	if classified["tool"] != "forecast" || classified["to"] != "enabled" ||
		classified["from"] != "pending" {
		t.Errorf("classification detail = %v, want forecast pending -> enabled", classified)
	}
	if classified["descriptor_hash"] != tools[0].Hash {
		t.Errorf("classification detail records hash %v, want %q",
			classified["descriptor_hash"], tools[0].Hash)
	}

	// Entries appended by this path must leave the chain verifiable; an audit
	// trail that breaks its own hash chain is worse than no entry.
	if broken, err := audit.VerifyChain(ctx); err != nil || broken != 0 {
		t.Fatalf("audit chain broken at seq %d (err %v)", broken, err)
	}
}

// A trail that records non-events is one nobody reads carefully. Setting a
// server to the state it is already in changes nothing and says nothing.
func TestMCPServerStore_NoAuditForAnUnchangedToggle(t *testing.T) {
	ctx := context.Background()
	s, db := newMCPStore(t)
	importFixture(t, s)

	// Imported servers start enabled, so enabling again is a no-op.
	if err := s.SetEnabled(ctx, "user:admin", "weather", true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	records, err := NewAuditStore(db).Recent(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range records {
		if r.Entry.Kind == "mcpserver.enabled" {
			t.Fatal("an unchanged toggle must not be recorded as a change")
		}
	}
}

// 0010 has to sort existing operations honestly in both directions, and it is
// the only chance to: the column is immutable afterwards and the table refuses
// deletion.
//
// One that declared a desired state really was compared against it, so
// recording it as unverifiable would rewrite settled history into "nobody
// checked". One that declared none was settled verified having read nothing,
// so blessing it would have the migration reassert the very claim it exists to
// remove.
func TestMigrate_BackfillsVerifiabilityHonestly(t *testing.T) {
	ctx := context.Background()

	db, err := Open(ctx, Options{
		Path:              filepath.Join(t.TempDir(), "upgrade.db"),
		RelaxedDurability: true,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			name       TEXT    NOT NULL,
			checksum   TEXT    NOT NULL,
			applied_at INTEGER NOT NULL
		) STRICT`); err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations[:len(migrations)-1] {
		if err := applyOne(ctx, db.Writer(), m); err != nil {
			t.Fatalf("apply %04d: %v", m.version, err)
		}
	}

	// Two settled operations, written the way the older schema knew how. The
	// first declared a desired state and really was compared against it. The
	// second declared none, which is exactly the row the old short circuit
	// settled as verified having read nothing -- reachable because the broken
	// schema check lived in the tool path, so an out-of-process plugin
	// registering only mutations mounted fine.
	if _, err := db.Writer().ExecContext(ctx, `
		INSERT INTO operations (
			id, plugin, action, state, risk, target_json, params_json, payload_hash,
			desired_json, precondition_json, impact, requested_by, requested_at,
			expires_at, outcome_verified, correlation_id, idempotency_key
		) VALUES
		  ('op_compared','echo','label.set','succeeded','low','{}','{}','h1',
		   '{"label":"b"}','{"label":"a"}','','user:alice',0,0,1,'corr','idem-1'),
		  ('op_uncompared','echo','thing.do','succeeded','low','{}','{}','h2',
		   NULL,'{"label":"a"}','','user:alice',0,0,1,'corr','idem-2')`,
	); err != nil {
		t.Fatalf("insert legacy operations: %v", err)
	}

	if _, err := Migrate(ctx, db); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	store := NewOperationStore(db, func() time.Time { return testClock })

	compared, err := store.Get(ctx, "op_compared")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !compared.Verifiable {
		t.Error("an operation that declared a desired state must stay verifiable")
	}
	if compared.Assurance() != operations.AssuranceReviewedChange {
		t.Errorf("assurance = %s, want reviewed_change", compared.Assurance())
	}

	// The migration must not bless a row nothing ever re-read. It is the only
	// chance to correct it: the column is immutable after this and the table
	// refuses deletion.
	uncompared, err := store.Get(ctx, "op_uncompared")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if uncompared.Verifiable {
		t.Error("an operation with no desired state was never verified; " +
			"0010 must not record it as verifiable")
	}
	if uncompared.Assurance() != operations.AssuranceGatedCall {
		t.Errorf("assurance = %s, want gated_call", uncompared.Assurance())
	}
}
