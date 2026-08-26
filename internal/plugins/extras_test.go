package plugins

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/auth"
)

func testRegistry() *Registry {
	return newRegistry(Descriptor{Name: "thing", Version: "1.0.0", Title: "Thing"})
}

// A tool is read unless it says otherwise. The override exists for the read
// that is not merely a read -- a credential dump, a billing figure -- where
// seeing it is itself the privilege.
func TestTool_CapabilityDefaultsToReadAndCanBeRaised(t *testing.T) {
	r := testRegistry()
	Tool(r, ToolSpec{Name: "get_plain", Title: "P", Description: "d"},
		func(context.Context, struct{}) (struct{}, error) { return struct{}{}, nil })
	Tool(r, ToolSpec{
		Name: "get_sensitive", Title: "S", Description: "d",
		Capability: auth.CapAdmin,
	}, func(context.Context, struct{}) (struct{}, error) { return struct{}{}, nil })

	if err := r.err(); err != nil {
		t.Fatalf("registration: %v", err)
	}
	got := map[string]auth.Capability{}
	for _, tool := range r.tools {
		got[tool.spec.Name] = tool.capability
	}
	if got["get_plain"] != auth.CapRead {
		t.Errorf("get_plain = %q, want read", got["get_plain"])
	}
	if got["get_sensitive"] != auth.CapAdmin {
		t.Errorf("get_sensitive = %q, want admin", got["get_sensitive"])
	}
}

func TestTool_RejectsAnUnknownCapability(t *testing.T) {
	r := testRegistry()
	Tool(r, ToolSpec{Name: "x", Title: "X", Description: "d", Capability: "wizard"},
		func(context.Context, struct{}) (struct{}, error) { return struct{}{}, nil })
	if r.err() == nil {
		t.Fatal("an unknown capability must be refused at registration")
	}
}

func TestTool_RejectsANegativeRateLimit(t *testing.T) {
	r := testRegistry()
	Tool(r, ToolSpec{Name: "x", Title: "X", Description: "d", RateLimit: -1},
		func(context.Context, struct{}) (struct{}, error) { return struct{}{}, nil })
	if r.err() == nil {
		t.Fatal("a negative rate limit must be refused")
	}
}

// A limiter is only built when one is asked for, so a tool with no limit costs
// nothing at call time.
func TestToolLimiter_UnboundedIsFree(t *testing.T) {
	if l := newToolLimiter(0); l.limiter != nil {
		t.Error("no limit must mean no limiter")
	}
	if err := newToolLimiter(0).allow(time.Now()); err != nil {
		t.Errorf("an unbounded limiter must never refuse: %v", err)
	}
	if l := newToolLimiter(5); l.limiter == nil {
		t.Error("a declared limit must produce a limiter")
	}
}

// The host binds the scheme, so one plugin cannot serve another's addresses.
func TestResource_HostBindsTheScheme(t *testing.T) {
	r := testRegistry()
	Resource(r, ResourceSpec{
		Path: "shares", Name: "shares", Description: "The shares.",
	}, func(context.Context) (string, error) { return "", nil })

	if err := r.err(); err != nil {
		t.Fatalf("registration: %v", err)
	}
	if len(r.resources) != 1 {
		t.Fatalf("registered %d resources, want 1", len(r.resources))
	}
	if got := r.resources[0].uri; got != "thing://shares" {
		t.Errorf("uri = %q, want the plugin's own scheme", got)
	}
}

func TestResource_RefusesAPathCarryingAScheme(t *testing.T) {
	r := testRegistry()
	Resource(r, ResourceSpec{
		Path: "other://shares", Name: "s", Description: "d",
	}, func(context.Context) (string, error) { return "", nil })

	err := r.err()
	if err == nil {
		t.Fatal("a path with its own scheme must be refused")
	}
	if !strings.Contains(err.Error(), "scheme") {
		t.Errorf("error = %q, want it to say why", err)
	}
}

// A model reading a list of resources has the description and nothing else.
func TestResource_RequiresADescription(t *testing.T) {
	r := testRegistry()
	Resource(r, ResourceSpec{Path: "x", Name: "x"},
		func(context.Context) (string, error) { return "", nil })
	if r.err() == nil {
		t.Fatal("a resource without a description must be refused")
	}
}

func TestResource_RefusesADuplicate(t *testing.T) {
	r := testRegistry()
	for range 2 {
		Resource(r, ResourceSpec{Path: "x", Name: "x", Description: "d"},
			func(context.Context) (string, error) { return "", nil })
	}
	if r.err() == nil {
		t.Fatal("the same resource twice must be refused")
	}
}

func TestPrompt_Registers(t *testing.T) {
	r := testRegistry()
	Prompt(r, PromptSpec{
		Name: "diagnose", Title: "Diagnose", Description: "Work through a device.",
		Args: []PromptArg{{Name: "mac", Required: true}},
	}, func(context.Context, map[string]string) (string, error) { return "", nil })

	if err := r.err(); err != nil {
		t.Fatalf("registration: %v", err)
	}
	if len(r.prompts) != 1 || r.prompts[0].qualified != "thing_diagnose" {
		t.Fatalf("prompts = %+v, want one qualified by plugin", r.prompts)
	}
	if r.prompts[0].capability != auth.CapRead {
		t.Errorf("capability = %q, want read: a prompt returns text and performs nothing",
			r.prompts[0].capability)
	}
}

func TestPrompt_RefusesADuplicateArgument(t *testing.T) {
	r := testRegistry()
	Prompt(r, PromptSpec{
		Name: "x", Description: "d",
		Args: []PromptArg{{Name: "a"}, {Name: "a"}},
	}, func(context.Context, map[string]string) (string, error) { return "", nil })
	if r.err() == nil {
		t.Fatal("the same argument twice must be refused")
	}
}

// A plugin with only resources or only prompts is a legitimate plugin. The
// check that it registered something has to know that.
func TestRegistry_ResourceOnlyPluginIsValid(t *testing.T) {
	m := testManager(t)
	p := &resourceOnlyPlugin{}
	if err := m.Register(context.Background(), p, "docs", false); err != nil {
		t.Fatalf("a resource-only plugin must be allowed: %v", err)
	}
}

type resourceOnlyPlugin struct{}

func (r *resourceOnlyPlugin) Descriptor() Descriptor {
	return Descriptor{Name: "docs", Version: "1.0.0", Title: "Docs"}
}

func (r *resourceOnlyPlugin) Register(_ context.Context, reg *Registry) error {
	Resource(reg, ResourceSpec{Path: "readme", Name: "readme", Description: "The readme."},
		func(context.Context) (string, error) { return "hello", nil })
	return nil
}

// A plugin prefixes its errors with what it is, and so does the host when it
// says which plugin failed. Left alone the two compose into "plugins:
// cnmaestro did not start with the new settings: cnmaestro: ..." -- the name
// twice, and the part that matters pushed to the end.
func TestOwnName_DoesNotRepeatThePluginName(t *testing.T) {
	inner := errors.New("cnmaestro: those credentials were rejected")

	got := ownName("cnmaestro", inner).Error()
	if want := "those credentials were rejected"; got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
	// Trimming is presentation. Anything matching on the original error still
	// has to find it.
	if !errors.Is(ownName("cnmaestro", inner), inner) {
		t.Fatal("the original error must still be wrapped")
	}
}

// An error that does not already name the plugin is left exactly as it is.
func TestOwnName_LeavesOtherErrorsAlone(t *testing.T) {
	inner := errors.New("dial tcp: connection refused")
	if got := ownName("cnmaestro", inner).Error(); got != inner.Error() {
		t.Fatalf("message = %q, want it untouched", got)
	}
	if ownName("cnmaestro", nil) != nil {
		t.Fatal("a nil error must stay nil")
	}
}
