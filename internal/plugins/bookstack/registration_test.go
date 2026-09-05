package bookstack

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/spoked/mcpd/internal/plugins"
)

// Registration is where the house rules are enforced -- the verb_resource tool
// name, the resource.verb mutation action, the derived schemas -- so this
// mounts the plugin the way the host does rather than calling handlers
// directly.
func TestEverythingRegisters(t *testing.T) {
	t.Parallel()
	p, err := New(testDeps(), Config{
		Host: "http://bookstack.example", TokenID: "tok", TokenSecret: "sec",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	m := plugins.NewManager(slog.New(slog.NewTextHandler(io.Discard, nil)), "test",
		nil, stubApprovals{}, nil, nil)
	if err := m.Register(context.Background(), p, "bookstack", false); err != nil {
		t.Fatalf("registering the plugin: %v", err)
	}
	mounted := m.Lookup("bookstack")
	if mounted == nil {
		t.Fatal("the plugin did not mount")
	}

	names := mounted.Registry.ToolNames()
	// The host prefixes the instance name, so what a model reads is
	// bookstack_list_books. The bare name is what the verb rule applies to.
	for _, name := range names {
		bare := strings.TrimPrefix(name, "bookstack_")
		if bare == name {
			t.Errorf("%q is not prefixed with the instance name", name)
		}
		ok := false
		for _, verb := range []string{"list_", "get_", "search_", "aggregate_"} {
			if strings.HasPrefix(bare, verb) {
				ok = true
			}
		}
		if !ok {
			t.Errorf("%q does not begin with a verb from the vocabulary", name)
		}
	}
	if len(names) < 20 {
		t.Fatalf("want the whole read surface, registered %d: %v", len(names), names)
	}
	t.Logf("%d tools and mutations registered", len(names))
}

// Reversible is not documentation. The host refuses to let a standing rule
// auto-approve an irreversible mutation, so a delete that wrongly claims a way
// back is a change that can happen with nobody watching.
//
// This is the list, written out, so that adding a mutation means deciding
// which side it is on rather than inheriting a default.
func TestIrreversibleMutationsSaySo(t *testing.T) {
	t.Parallel()

	// BookStack keeps no copy of these: no revision, no recycle bin.
	irreversible := map[string]bool{
		"recycle_bin.destroy": true,
		"comment.delete":      true,
		"attachment.delete":   true,
		"user.delete":         true,
		"role.delete":         true,
	}
	// These go to the recycle bin, from which restore_from_recycle_bin puts
	// them back, so the claim is honest.
	reversible := map[string]bool{
		"page.delete": true, "book.delete": true,
		"chapter.delete": true, "shelf.delete": true,
	}

	specs := mutationSpecs(t)
	if len(specs) == 0 {
		t.Fatal("no mutations were collected")
	}
	for action, spec := range specs {
		switch {
		case irreversible[action]:
			if spec.Reversible {
				t.Errorf("%s destroys and cannot be undone, but claims Reversible; "+
					"that lets a standing rule approve it unattended", action)
			}
		case reversible[action]:
			if !spec.Reversible {
				t.Errorf("%s goes to the recycle bin, so it is reversible", action)
			}
		}
		if !spec.Verifiable {
			t.Errorf("%s does not claim Verifiable, but every mutation here has a "+
				"read that confirms it", action)
		}
		if spec.Description == "" {
			t.Errorf("%s has no description", action)
		}
	}
	for action := range irreversible {
		if _, ok := specs[action]; !ok {
			t.Errorf("%s is not registered; this list has drifted from the code", action)
		}
	}
}

// Every deletion that destroys should be at the top of the risk scale, and
// every change to who can see what should be near it.
func TestRiskIsClassified(t *testing.T) {
	t.Parallel()
	wantAtLeast := map[string]string{
		"recycle_bin.destroy":        "critical",
		"user.delete":                "critical",
		"role.delete":                "critical",
		"role.update":                "critical",
		"book.delete":                "high",
		"chapter.delete":             "high",
		"content_permissions.update": "high",
		"user.create":                "high",
		"user.update":                "high",
	}
	rank := map[string]int{"low": 0, "medium": 1, "high": 2, "critical": 3}
	for action, want := range wantAtLeast {
		spec, ok := mutationSpecs(t)[action]
		if !ok {
			t.Errorf("%s is not registered", action)
			continue
		}
		if rank[string(spec.Risk)] < rank[want] {
			t.Errorf("%s is %s, want at least %s", action, spec.Risk, want)
		}
	}
}

// mutationSpecs is every declaration the plugin makes, by action.
func mutationSpecs(t *testing.T) map[string]plugins.MutationSpec {
	t.Helper()
	p, err := New(testDeps(), Config{
		Host: "http://bookstack.example", TokenID: "tok", TokenSecret: "sec",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	out := map[string]plugins.MutationSpec{}
	for _, m := range p.mutations() {
		if _, clash := out[m.Spec.Action]; clash {
			t.Fatalf("two mutations both call themselves %q", m.Spec.Action)
		}
		out[m.Spec.Action] = m.Spec
	}
	return out
}
