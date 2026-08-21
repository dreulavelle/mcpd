package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type echoIn struct {
	Message string `json:"message" jsonschema:"text to echo"`
	Times   int    `json:"times,omitempty"`
}

type echoOut struct {
	Result string `json:"result"`
}

func newTestPlugin() *Plugin {
	p := New("test", "1.0.0", "Test", "A plugin for tests.")
	Tool(p, ToolSpec{
		Name: "echo", Title: "Echo", Description: "Echoes a message.", Idempotent: true,
	}, func(_ context.Context, in echoIn) (echoOut, error) {
		if in.Message == "" {
			return echoOut{}, fmt.Errorf("message is required")
		}
		return echoOut{Result: in.Message}, nil
	})
	return p
}

// call drives one request through the serve loop.
func call(t *testing.T, p *Plugin, method string, params any) response {
	t.Helper()

	var raw json.RawMessage
	if params != nil {
		encoded, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		raw = encoded
	}
	req, err := json.Marshal(request{ID: 1, Method: method, Params: raw})
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	_ = p.serve(bytes.NewReader(append(req, '\n')), &out)

	var resp response
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &resp); err != nil {
		t.Fatalf("could not decode the response %q: %v", out.String(), err)
	}
	return resp
}

func TestDescribe(t *testing.T) {
	resp := call(t, newTestPlugin(), "describe", nil)
	if resp.Error != nil {
		t.Fatal(resp.Error)
	}

	var d describeResult
	if err := json.Unmarshal(resp.Result, &d); err != nil {
		t.Fatal(err)
	}
	if d.Protocol != protocolVersion {
		t.Fatalf("protocol = %q, want %q", d.Protocol, protocolVersion)
	}
	if d.Name != "test" || len(d.Tools) != 1 {
		t.Fatalf("describe = %+v", d)
	}
	if d.Tools[0].InputSchema == nil {
		t.Fatal("a tool must publish an input schema so a model can construct a call")
	}
}

func TestCallTool(t *testing.T) {
	p := newTestPlugin()

	resp := call(t, p, "call_tool", callToolParams{
		Name: "echo", Args: json.RawMessage(`{"message":"hi"}`),
	})
	if resp.Error != nil {
		t.Fatal(resp.Error)
	}
	if !strings.Contains(string(resp.Result), "hi") {
		t.Fatalf("result = %s", resp.Result)
	}

	// The plugin's own validation must surface as a protocol error.
	resp = call(t, p, "call_tool", callToolParams{
		Name: "echo", Args: json.RawMessage(`{"message":""}`),
	})
	if resp.Error == nil {
		t.Fatal("the handler's error must reach the host")
	}

	resp = call(t, p, "call_tool", callToolParams{Name: "nope"})
	if resp.Error == nil || resp.Error.Code != "INVALID_PARAMS" {
		t.Fatalf("unknown tool should be INVALID_PARAMS, got %+v", resp.Error)
	}
}

// A panic in plugin code must become an error response, not a dead process
// mid-conversation.
func TestPanicBecomesAnError(t *testing.T) {
	p := New("boom", "1.0.0", "Boom", "x")
	Tool(p, ToolSpec{Name: "explode", Description: "Panics."},
		func(context.Context, struct{}) (struct{}, error) { panic("kaboom") })

	resp := call(t, p, "call_tool", callToolParams{Name: "explode", Args: json.RawMessage(`{}`)})
	if resp.Error == nil || resp.Error.Code != "INTERNAL" {
		t.Fatalf("a panic should surface as INTERNAL, got %+v", resp.Error)
	}
	if !strings.Contains(resp.Error.Message, "kaboom") {
		t.Fatalf("the panic value should be reported: %s", resp.Error.Message)
	}
}

// The single most important behaviour in the SDK: an ambiguous outcome must
// travel to the host as INDETERMINATE so the change is never retried.
func TestIndeterminateIsMappedOnTheWire(t *testing.T) {
	p := New("m", "1.0.0", "M", "x")
	RegisterMutation(p, MutationSpec{
		Action: "thing.do", Title: "Do", Description: "Does.", Risk: RiskLow,
	}, ambiguousMutation{})

	resp := call(t, p, "apply", mutationParams{
		Action: "thing.do", Params: json.RawMessage(`{}`),
	})
	if resp.Error == nil {
		t.Fatal("expected an error")
	}
	if resp.Error.Code != "INDETERMINATE" {
		t.Fatalf("code = %q, want INDETERMINATE; anything else lets the host "+
			"retry a change that may already have been applied", resp.Error.Code)
	}
}

type ambiguousMutation struct{}

func (ambiguousMutation) Plan(context.Context, struct{}) (Plan[struct{}], error) {
	return Plan[struct{}]{Impact: "x"}, nil
}
func (ambiguousMutation) Apply(context.Context, struct{}, Plan[struct{}]) (ApplyResult, error) {
	return ApplyResult{}, fmt.Errorf("the write may have landed: %w", Indeterminate)
}
func (ambiguousMutation) Observe(context.Context, struct{}) (struct{}, error) {
	return struct{}{}, nil
}

func TestCodeFor(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{fmt.Errorf("wrapped: %w", Indeterminate), "INDETERMINATE"},
		{fmt.Errorf("wrapped: %w", NotFound), "NOT_FOUND"},
		{invalidParams("bad"), "INVALID_PARAMS"},
		{errors.New("something broke"), "UPSTREAM_FAILED"},
	}
	for _, tc := range tests {
		if got := codeFor(tc.err); got != tc.want {
			t.Errorf("codeFor(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

// Registration mistakes must be reported together and must stop the plugin
// from serving. A half-registered plugin is worse than one that refuses.
func TestValidate_CollectsRegistrationErrors(t *testing.T) {
	p := New("Bad Name", "", "T", "x")
	Tool(p, ToolSpec{Name: "BadTool", Description: "x"},
		func(context.Context, struct{}) (struct{}, error) { return struct{}{}, nil })
	Tool(p, ToolSpec{Name: "nodesc"},
		func(context.Context, struct{}) (struct{}, error) { return struct{}{}, nil })

	err := p.validate()
	if err == nil {
		t.Fatal("expected registration to fail")
	}
	for _, want := range []string{"name", "version", "description"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("errors should mention %q: %v", want, err)
		}
	}
}

func TestValidate_RequiresSomethingToServe(t *testing.T) {
	if err := New("empty", "1.0.0", "E", "x").validate(); err == nil {
		t.Fatal("a plugin exposing nothing must not start")
	}
}

func TestRegisterMutation_Validates(t *testing.T) {
	tests := []struct {
		name string
		spec MutationSpec
	}{
		{"action without a dot", MutationSpec{Action: "reboot", Description: "x", Risk: RiskLow}},
		{"no description", MutationSpec{Action: "a.b", Risk: RiskLow}},
		{"invalid risk", MutationSpec{Action: "a.b", Description: "x", Risk: "catastrophic"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := New("p", "1.0.0", "P", "x")
			RegisterMutation(p, tc.spec, ambiguousMutation{})
			if len(p.errs) == 0 {
				t.Fatal("expected a registration error")
			}
		})
	}
}

func TestSchemaGeneration(t *testing.T) {
	raw := schemaFor[echoIn]()

	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	if schema["type"] != "object" {
		t.Fatalf("type = %v", schema["type"])
	}

	props, _ := schema["properties"].(map[string]any)
	message, _ := props["message"].(map[string]any)
	if message["type"] != "string" {
		t.Fatalf("message type = %v", message["type"])
	}
	if message["description"] != "text to echo" {
		t.Fatalf("the jsonschema tag should become a description, got %v", message["description"])
	}

	// omitempty means optional; everything else is required.
	required, _ := schema["required"].([]any)
	if len(required) != 1 || required[0] != "message" {
		t.Fatalf("required = %v, want just message", required)
	}
}

// json.RawMessage is a []byte but carries arbitrary JSON, so describing it as
// an array of integers would make every call fail validation.
func TestSchemaGeneration_RawMessageIsOpaque(t *testing.T) {
	type withRaw struct {
		Payload json.RawMessage `json:"payload"`
	}
	var schema map[string]any
	if err := json.Unmarshal(schemaFor[withRaw](), &schema); err != nil {
		t.Fatal(err)
	}
	props := schema["properties"].(map[string]any)
	payload := props["payload"].(map[string]any)
	if payload["type"] == "array" {
		t.Fatal("json.RawMessage must not be described as an array")
	}
}

// A self-referential type must not recurse until the stack gives out.
func TestSchemaGeneration_BoundsRecursion(t *testing.T) {
	type node struct {
		Name  string `json:"name"`
		Child *node  `json:"child,omitempty"`
	}
	if raw := schemaFor[node](); len(raw) == 0 {
		t.Fatal("expected a schema")
	}
}

func TestSchemaGeneration_SkipsUnexportedAndDashed(t *testing.T) {
	type mixed struct {
		Public  string `json:"public"`
		Skipped string `json:"-"`
		private string
	}
	var schema map[string]any
	_ = json.Unmarshal(schemaFor[mixed](), &schema)
	props := schema["properties"].(map[string]any)

	if _, ok := props["public"]; !ok {
		t.Error("exported field missing")
	}
	if _, ok := props["Skipped"]; ok {
		t.Error(`a json:"-" field must be omitted`)
	}
	if len(props) != 1 {
		t.Errorf("properties = %v, want only the public field", props)
	}
	_ = mixed{private: ""}
}

func TestHealthDefaultsToHealthy(t *testing.T) {
	resp := call(t, newTestPlugin(), "health", nil)
	if resp.Error != nil {
		t.Fatal(resp.Error)
	}
	var h Health
	_ = json.Unmarshal(resp.Result, &h)
	if h.State != "healthy" {
		t.Fatalf("state = %q, want healthy when no check is registered", h.State)
	}
}

func TestUnknownMethod(t *testing.T) {
	resp := call(t, newTestPlugin(), "teleport", nil)
	if resp.Error == nil || resp.Error.Code != "INVALID_PARAMS" {
		t.Fatalf("error = %+v", resp.Error)
	}
}

// A frame that cannot be parsed has no id to answer, so it is logged and the
// loop continues rather than desynchronising.
func TestUnreadableFrameDoesNotStopTheLoop(t *testing.T) {
	p := newTestPlugin()

	input := "{ not json\n" +
		`{"id":7,"method":"health"}` + "\n"
	var out bytes.Buffer
	_ = p.serve(strings.NewReader(input), &out)

	if !strings.Contains(out.String(), `"id":7`) {
		t.Fatalf("the valid request after a bad frame was not answered: %s", out.String())
	}
}

// A plugin declares what it needs configured; the host resolves it and hands
// back values, so a plugin never reads a file or an environment variable.
func TestSettingsAndConfigure(t *testing.T) {
	p := New("thing", "1.0.0", "Thing", "A thing.")
	Settings(p,
		SettingField{Key: "api_token", Label: "API token", Kind: KindSecret, Required: true},
		SettingField{Key: "host", Label: "Address", Kind: KindString, Default: "example.test"},
	)
	Tool(p, ToolSpec{Name: "noop", Description: "Does nothing."},
		func(context.Context, struct{}) (struct{}, error) { return struct{}{}, nil })

	d := p.describe()
	if len(d.Settings) != 2 {
		t.Fatalf("described %d settings, want 2", len(d.Settings))
	}
	if d.Settings[0].Kind != KindSecret || !d.Settings[0].Required {
		t.Errorf("first setting = %+v, want a required secret", d.Settings[0])
	}

	// Nothing is configured until the host says so.
	if _, ok := p.Configured("api_token"); ok {
		t.Error("a setting must not report configured before the host hands values over")
	}

	resp := p.dispatch(context.Background(), request{
		Method: "configure",
		Params: []byte(`{"api_token":"secret-value","host":"nas.local"}`),
	})
	if resp.Error != nil {
		t.Fatalf("configure: %v", resp.Error)
	}

	if v, ok := p.Configured("api_token"); !ok || v != "secret-value" {
		t.Errorf("api_token = %q,%v want the resolved value", v, ok)
	}
	// An empty value is "not set", which a plugin can act on.
	if _, ok := p.Configured("nonesuch"); ok {
		t.Error("an unset field must report not configured")
	}
}

// A bad field declaration is a developer's mistake, caught before Serve rather
// than when someone opens the page.
func TestSettings_RejectsABadField(t *testing.T) {
	for _, f := range []SettingField{
		{Key: "Bad-Key", Label: "X", Kind: KindString},
		{Key: "ok", Kind: KindString},
		{Key: "ok", Label: "X", Kind: "colour"},
		{Key: "ok", Label: "X", Kind: KindEnum},
	} {
		p := New("thing", "1.0.0", "Thing", "A thing.")
		Settings(p, f)
		if len(p.errs) == 0 {
			t.Errorf("field %+v must be refused", f)
		}
	}
}

// A resource is reference material a model reads by address, which keeps it
// out of the tool catalogue where every entry costs attention on every call.
func TestResourceAndPrompt(t *testing.T) {
	p := New("thing", "1.0.0", "Thing", "A thing.")
	Resource(p, ResourceSpec{
		Path: "state", Name: "state", Description: "Current state.",
		MIMEType: "application/json",
	}, func(context.Context) (string, error) { return `{"ok":true}`, nil })

	Prompt(p, PromptSpec{
		Name: "diagnose", Description: "Work through a device.",
		Args: []PromptArg{{Name: "mac", Required: true}},
	}, func(_ context.Context, args map[string]string) (string, error) {
		return "look at " + args["mac"], nil
	})

	d := p.describe()
	if len(d.Resources) != 1 || d.Resources[0].Path != "state" {
		t.Fatalf("resources = %+v", d.Resources)
	}
	if d.Resources[0].MIMEType != "application/json" {
		t.Errorf("mime = %q, want the declared one", d.Resources[0].MIMEType)
	}
	if len(d.Prompts) != 1 || len(d.Prompts[0].Args) != 1 {
		t.Fatalf("prompts = %+v", d.Prompts)
	}

	resp := p.dispatch(context.Background(), request{
		Method: "read_resource", Params: []byte(`{"path":"state"}`),
	})
	if resp.Error != nil {
		t.Fatalf("read_resource: %v", resp.Error)
	}
	if !strings.Contains(string(resp.Result), "ok") {
		t.Errorf("resource body = %s", resp.Result)
	}

	resp = p.dispatch(context.Background(), request{
		Method: "get_prompt", Params: []byte(`{"name":"diagnose","args":{"mac":"AA:BB"}}`),
	})
	if resp.Error != nil {
		t.Fatalf("get_prompt: %v", resp.Error)
	}
	if !strings.Contains(string(resp.Result), "look at AA:BB") {
		t.Errorf("prompt text = %s", resp.Result)
	}
}

// Returning text rather than performing anything is the whole contract of a
// prompt, so a missing required argument is refused before the handler runs.
func TestPrompt_RefusesAPathCarryingAScheme(t *testing.T) {
	p := New("thing", "1.0.0", "Thing", "A thing.")
	Resource(p, ResourceSpec{Path: "other://x", Name: "x", Description: "d"},
		func(context.Context) (string, error) { return "", nil })
	if len(p.errs) == 0 {
		t.Fatal("a resource path carrying its own scheme must be refused")
	}
}
