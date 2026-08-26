package plugins

import (
	"context"
	"strings"
	"testing"
)

// The house rule, checked where it is enforced rather than once per plugin.
//
// The host prefixes every tool with the instance name, so the name a model
// actually reads is service_verb_resource. A bare noun makes that a service
// and a category with no action in it; a bare verb makes it a service and an
// action with nothing to act on, which is unambiguous only until the plugin
// gains a second thing that verb could apply to.
func TestCheckToolName_HoldsTheVerbResourceRule(t *testing.T) {
	builtin := Descriptor{Name: "graylog", Version: "1.0.0", Title: "Graylog"}

	for _, tc := range []struct {
		name  string
		wants string // substring the refusal must carry, "" for accepted
	}{
		{"search_messages", ""},
		{"list_streams", ""},
		{"get_system_status", ""},
		{"aggregate_messages", ""},

		// The two shapes this exists to stop. Both shipped here before it did.
		{"devices", "names no verb"},
		{"indicators", "names no verb"},
		{"search", "bare verb"},
		{"list", "bare verb"},

		// A verb that is not in the vocabulary. Adding one is an edit to
		// toolVerbs, which is the point: a vocabulary that grows by accident
		// stops being one.
		{"fetch_messages", "names no verb"},

		// The charset rule still applies and is reported on its own terms.
		{"Search_Messages", "must match"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkToolName(builtin, tc.name)
			switch {
			case tc.wants == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case tc.wants != "" && err == nil:
				t.Fatalf("accepted; it should have said %q", tc.wants)
			case tc.wants != "" && !strings.Contains(err.Error(), tc.wants):
				t.Errorf("message = %v, want it to mention %q", err, tc.wants)
			}
		})
	}
}

// A refusal has to say what to do, not only that something is wrong. The
// author is looking at a name that reads fine on its own; what they cannot see
// from the call site is the prefix the host is about to add.
func TestCheckToolName_ShowsTheNameAModelWouldSee(t *testing.T) {
	err := checkToolName(Descriptor{Name: "observium", Version: "1", Title: "O"}, "devices")
	if err == nil {
		t.Fatal("a bare noun was accepted")
	}
	if !strings.Contains(err.Error(), "observium_devices") {
		t.Errorf("the refusal should quote the qualified name, got: %v", err)
	}
}

// A remote MCP server's tools are named by whoever wrote it. Holding them to
// this host's vocabulary would mean either refusing to mount an ordinary
// server or renaming its tools -- and a renamed tool is one the far end does
// not answer to, so every call would fail at the last hop with nothing saying
// why.
func TestCheckToolName_DoesNotBindARemoteServer(t *testing.T) {
	remote := Descriptor{
		Name: "weather", Version: "1.0.0", Title: "Weather", Runtime: RuntimeMCP,
	}
	for _, name := range []string{"getWeather", "search.docs", "read-file", "forecast"} {
		if err := checkToolName(remote, name); err != nil {
			t.Errorf("a remote server's %q was refused: %v", name, err)
		}
	}
}

// The rule is enforced at registration, which for a compiled-in plugin is
// startup and for an out-of-process one is the moment it mounts. Checked
// through Tool rather than only against checkToolName, because a check nothing
// calls is not one.
func TestTool_RefusesANameOutsideTheVocabulary(t *testing.T) {
	r := newRegistry(Descriptor{Name: "thing", Version: "1.0.0", Title: "Thing"})
	Tool(r, ToolSpec{Name: "widgets", Title: "Widgets", Description: "Lists widgets."},
		func(context.Context, struct{}) (struct{}, error) { return struct{}{}, nil })

	err := r.err()
	if err == nil {
		t.Fatal("registration accepted a bare noun")
	}
	if !strings.Contains(err.Error(), "names no verb") {
		t.Errorf("error = %v, want the vocabulary rule", err)
	}
}

// Mutations are deliberately outside this rule. Their identifier is the
// approval policy's before it is a model's: "device.reboot" is what an
// administrator writes a standing rule against and what the audit trail
// records, and reordering those words would silently stop stored rules
// matching. A rule that quietly stops matching is an exclusion that quietly
// stops excluding.
func TestMutation_KeepsTheResourceVerbActionNamespace(t *testing.T) {
	r := newRegistry(Descriptor{Name: "thing", Version: "1.0.0", Title: "Thing"})
	Mutation(r, MutationSpec{
		Action: "device.reboot", Title: "Reboot", Description: "Reboots a device.",
		Risk: "high",
	}, &countingMutation{})

	if err := r.err(); err != nil {
		t.Fatalf("a resource.verb action was refused: %v", err)
	}
	if got := r.MutationActions(); len(got) != 1 || got[0] != "device.reboot" {
		t.Fatalf("actions = %v, want device.reboot unchanged", got)
	}
}
