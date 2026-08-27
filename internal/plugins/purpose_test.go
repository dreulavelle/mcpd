package plugins

import (
	"context"
	"strings"
	"testing"
)

// What an instance covers reaches a model in two places, and both are free:
// the instructions a client reads once at connect, and the tool descriptions
// it is already reading when it decides which tool to call. Neither costs a
// call, which is why this is not a tool of its own.
func TestPurpose_ReachesTheToolDescription(t *testing.T) {
	m := testManager(t)
	m.SetPurposeSource(func(instance string) string {
		if instance == "nas-primary" {
			return "the Springfield branch"
		}
		return ""
	})
	ctx := context.Background()

	for _, name := range []string{"nas-primary", "nas-backup"} {
		if err := m.Register(ctx, &stubPlugin{}, name, false); err != nil {
			t.Fatalf("Register %s: %v", name, err)
		}
	}

	described := m.Lookup("nas-primary").Registry.tools[0]
	if got := described.spec.Description; got != "Lists shares." {
		t.Errorf("the spec was rewritten in place: %q", got)
	}
	if got := m.Lookup("nas-primary").Descriptor.Purpose; got != "the Springfield branch" {
		t.Errorf("descriptor purpose = %q, want the operator's words", got)
	}
	// The other instance has none, and gets nothing added: an integration
	// configured once is already unambiguous, and a line repeated across its
	// tools to restate its own name costs context and buys nothing.
	if got := m.Lookup("nas-backup").Descriptor.Purpose; got != "" {
		t.Errorf("purpose = %q, want none", got)
	}
}

func TestDescribeTool(t *testing.T) {
	for _, tc := range []struct {
		name, purpose, description, want string
	}{
		{"no purpose", "", "Lists shares.", "Lists shares."},
		{"appended as a phrase", "the Springfield branch", "Lists shares.",
			"Lists shares. the Springfield branch."},
		{"whitespace only", "   ", "Lists shares.", "Lists shares."},
		{"nothing to append to", "the Springfield branch", "", "the Springfield branch."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := describeTool(tc.purpose, tc.description); got != tc.want {
				t.Errorf("describeTool = %q, want %q", got, tc.want)
			}
		})
	}
}

// The purpose leads the instructions. A description says what an integration
// is -- the same words for both instances -- while the purpose says which one
// this is.
func TestInstructionsFor(t *testing.T) {
	d := Descriptor{Description: "Reads a Synology.", Purpose: "the Springfield branch"}
	if got, want := instructionsFor(d), "the Springfield branch. Reads a Synology."; got != want {
		t.Errorf("instructions = %q, want %q", got, want)
	}
	if got := instructionsFor(Descriptor{Description: "Reads a Synology."}); got != "Reads a Synology." {
		t.Errorf("instructions = %q, want the description untouched", got)
	}
}

// The aggregate endpoint is where two instances of one integration are most
// confusable: one tool list, two identical descriptions, and only the prefix
// to tell them apart.
func TestAggregateInstructions_NameWhatEachInstanceCovers(t *testing.T) {
	m := testManager(t)
	m.SetPurposeSource(func(instance string) string {
		switch instance {
		case "nas-primary":
			return "the Springfield branch"
		case "nas-backup":
			return "the Northgate office"
		}
		return ""
	})
	ctx := context.Background()
	for _, name := range []string{"nas-primary", "nas-backup"} {
		if err := m.Register(ctx, &stubPlugin{}, name, false); err != nil {
			t.Fatalf("Register %s: %v", name, err)
		}
	}

	got := m.aggregateInstructions([]string{"nas-primary", "nas-backup"})
	for _, want := range []string{
		"- nas-primary: the Springfield branch.",
		"- nas-backup: the Northgate office.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("instructions = %q, want a line %q", got, want)
		}
	}
}

// An edit has to be visible without a restart, which means the value is read
// when the plugin is built rather than captured when the host started.
func TestPurpose_IsRereadOnRemount(t *testing.T) {
	m := testManager(t)
	purpose := ""
	m.SetPurposeSource(func(string) string { return purpose })
	ctx := context.Background()

	if err := m.Register(ctx, &stubPlugin{}, "nas-primary", false); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if got := m.Lookup("nas-primary").Descriptor.Purpose; got != "" {
		t.Fatalf("purpose = %q before anything was written", got)
	}

	purpose = "the Springfield branch"
	if err := m.Remount(ctx, "nas-primary", &stubPlugin{}, false); err != nil {
		t.Fatalf("Remount: %v", err)
	}
	if got := m.Lookup("nas-primary").Descriptor.Purpose; got != "the Springfield branch" {
		t.Errorf("purpose = %q after a remount, want the edited value", got)
	}
}
