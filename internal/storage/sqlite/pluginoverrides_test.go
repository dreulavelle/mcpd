package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func newOverrideStore(t *testing.T) (*PluginOverrideStore, *AuditStore, *DB) {
	t.Helper()
	db := newTestDB(t)
	at := time.Unix(1_700_000_000, 0)
	return NewPluginOverrideStore(db, func() time.Time { return at }), NewAuditStore(db), db
}

var echoDeclaration = PluginDeclaration{Type: "echo", Enabled: true}

// The whole point of the table: a removal outlives the process, so the file's
// declaration is ignored on this start and on every one after it.
func TestPluginOverrides_RemoveIsRecordedAndAudited(t *testing.T) {
	store, audit, _ := newOverrideStore(t)
	ctx := context.Background()

	if err := store.Remove(ctx, "user:alice", "echo", echoDeclaration); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	list, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d overrides, want 1", len(list))
	}
	got := list[0]
	if !got.Removed || got.Enabled != nil {
		t.Fatalf("override = %+v, want removed with no enabled override", got)
	}
	if got.Actor != "user:alice" || got.DeclaredType != "echo" || got.UpdatedAt == 0 {
		t.Fatalf("override = %+v, want it to say who, what and when", got)
	}

	entries, err := audit.Recent(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d audit entries, want the removal to be one", len(entries))
	}
	e := entries[0]
	if e.Entry.Kind != "plugin.removed" || e.Entry.Plugin != "echo" || e.Entry.Actor != "user:alice" {
		t.Fatalf("audit entry = %+v, want the removal named with its actor", e)
	}
	// The entry has to be readable years later, when the file may say
	// something else entirely or nothing at all.
	for _, want := range []string{`"declared_type":"echo"`, `"declared_required":false`} {
		if !strings.Contains(string(e.Entry.Detail), want) {
			t.Errorf("audit detail %s is missing %s", e.Entry.Detail, want)
		}
	}
	if bad, err := audit.VerifyChain(ctx); err != nil || bad != 0 {
		t.Fatalf("VerifyChain = %d, %v; want an intact chain", bad, err)
	}
}

// A trail that records non-events is one nobody reads carefully.
func TestPluginOverrides_RemovingTwiceRecordsOnce(t *testing.T) {
	store, audit, _ := newOverrideStore(t)
	ctx := context.Background()

	for range 3 {
		if err := store.Remove(ctx, "user:alice", "echo", echoDeclaration); err != nil {
			t.Fatalf("Remove: %v", err)
		}
	}
	entries, err := audit.Recent(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d audit entries, want only the removal that changed something", len(entries))
	}
}

// Reversible, and reversible to the file rather than to a copy of it: the row
// goes away entirely, so what comes back is whatever the file declares now.
func TestPluginOverrides_Restore(t *testing.T) {
	store, audit, _ := newOverrideStore(t)
	ctx := context.Background()

	if err := store.Remove(ctx, "user:alice", "echo", echoDeclaration); err != nil {
		t.Fatal(err)
	}
	if err := store.Restore(ctx, "user:bob", "echo", true); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	list, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("got %+v, want the override forgotten entirely", list)
	}
	entries, err := audit.Recent(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Entry.Kind != "plugin.restored" {
		t.Fatalf("audit = %+v, want the restore recorded too", entries)
	}
	if !strings.Contains(string(entries[0].Entry.Detail), `"declared":true`) {
		t.Errorf("audit detail %s does not say the file still declared it", entries[0].Entry.Detail)
	}
}

// One action name, two acts. Forgetting a removal whose declaration has left
// the file adds nothing back, and an entry read years later cannot ask the file
// which it was -- so the entry says.
func TestPluginOverrides_RestoreRecordsWhetherAnythingWasDeclared(t *testing.T) {
	store, audit, _ := newOverrideStore(t)
	ctx := context.Background()

	if err := store.Remove(ctx, "user:alice", "gone", echoDeclaration); err != nil {
		t.Fatal(err)
	}
	if err := store.Restore(ctx, "user:alice", "gone", false); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	entries, err := audit.Recent(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	detail := string(entries[0].Entry.Detail)
	if !strings.Contains(detail, `"declared":false`) {
		t.Errorf("audit detail %s does not say the declaration was gone", detail)
	}
	// Nothing came back, so nothing may claim it came back to anything.
	if strings.Contains(detail, "restored_to") {
		t.Errorf("audit detail %s names what it was restored to, and it was not", detail)
	}
}

// Restoring something that is not removed is a claim that lost a race, or a
// mistake. Either way it must not report success.
func TestPluginOverrides_RestoreWithoutARemoval(t *testing.T) {
	store, _, _ := newOverrideStore(t)
	ctx := context.Background()

	if err := store.Restore(ctx, "user:alice", "echo", true); !errors.Is(err, ErrNotRemoved) {
		t.Fatalf("Restore = %v, want ErrNotRemoved", err)
	}
	// Nor when the row exists for another reason.
	if err := store.SetEnabled(ctx, "user:alice", "echo", echoDeclaration, false); err != nil {
		t.Fatal(err)
	}
	if err := store.Restore(ctx, "user:alice", "echo", true); !errors.Is(err, ErrNotRemoved) {
		t.Fatalf("Restore = %v, want ErrNotRemoved", err)
	}
}

// A removal keeps whatever else the row said, and a restore puts the plugin
// back exactly as far as the removal took it -- switched off, if that is how
// it was.
func TestPluginOverrides_RestoreKeepsTheEnabledOverride(t *testing.T) {
	store, _, _ := newOverrideStore(t)
	ctx := context.Background()

	if err := store.SetEnabled(ctx, "user:alice", "echo", echoDeclaration, false); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(ctx, "user:alice", "echo", echoDeclaration); err != nil {
		t.Fatal(err)
	}
	if err := store.Restore(ctx, "user:alice", "echo", true); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	list, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Removed || list[0].Enabled == nil || *list[0].Enabled {
		t.Fatalf("got %+v, want the row kept, not removed, still switched off", list)
	}
}

func TestPluginOverrides_SetEnabled(t *testing.T) {
	store, audit, _ := newOverrideStore(t)
	ctx := context.Background()

	tests := []struct {
		name    string
		enabled bool
		want    string
	}{
		{"off", false, "plugin.disabled"},
		{"on", true, "plugin.enabled"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := store.SetEnabled(ctx, "user:alice", "echo", echoDeclaration, tc.enabled); err != nil {
				t.Fatalf("SetEnabled: %v", err)
			}
			list, err := store.List(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(list) != 1 || list[0].Enabled == nil || *list[0].Enabled != tc.enabled {
				t.Fatalf("got %+v, want enabled=%v stored", list, tc.enabled)
			}
			entries, err := audit.Recent(ctx, 1)
			if err != nil {
				t.Fatal(err)
			}
			if entries[0].Entry.Kind != tc.want {
				t.Fatalf("audit kind = %q, want %q", entries[0].Entry.Kind, tc.want)
			}
		})
	}

	// Setting it to what it already says changes nothing and records nothing.
	before, err := audit.Recent(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetEnabled(ctx, "user:alice", "echo", echoDeclaration, true); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	after, err := audit.Recent(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("got %d entries, want the no-op unrecorded (%d)", len(after), len(before))
	}
}

// Switching a removed plugin on would be a toggle reporting success over
// something that is not being served either way. The guard is in the WHERE
// clause, so it is answered by the write rather than by a prior read.
func TestPluginOverrides_SetEnabledRefusesARemovedPlugin(t *testing.T) {
	store, _, _ := newOverrideStore(t)
	ctx := context.Background()

	if err := store.Remove(ctx, "user:alice", "echo", echoDeclaration); err != nil {
		t.Fatal(err)
	}
	if err := store.SetEnabled(ctx, "user:alice", "echo", echoDeclaration, true); !errors.Is(err, ErrPluginRemoved) {
		t.Fatalf("SetEnabled = %v, want ErrPluginRemoved", err)
	}
	list, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || !list[0].Removed || list[0].Enabled != nil {
		t.Fatalf("got %+v, want the removal untouched", list)
	}
}

// A row that overrides nothing is a name reserved for no reason. The database
// refuses one, so forgetting to delete it on restore is a hard error rather
// than a slow accumulation of rows meaning nothing.
func TestPluginOverrides_ARowMustOverrideSomething(t *testing.T) {
	_, _, db := newOverrideStore(t)
	_, err := db.Writer().ExecContext(context.Background(), `
		INSERT INTO plugin_overrides
			(name, removed, enabled, declared_type, actor, created_at, updated_at)
		VALUES ('echo', 0, NULL, 'echo', 'user:alice', 1, 1)`)
	if err == nil {
		t.Fatal("a row overriding nothing must be refused")
	}
}
