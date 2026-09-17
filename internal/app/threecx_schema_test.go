package app

import (
	"encoding/json"
	"strings"
	"testing"
)

// The 3CX tool schemas as an MCP client is handed them.
//
// The plugin's own tests check what the resolver decides; this checks what a
// model can tell *before* it calls anything, which is the part no amount of
// good behaviour inside a tool can fix. A tool that takes a customer but no
// system cannot be pointed at the second of a business's two phone systems,
// and an answer that does not name the system it came from cannot be corrected
// afterwards by anybody -- so both are structural, and both are checked over
// the wire rather than by reading the Go types.
func TestThreecx_SchemasCanTargetAndLabelAPhoneSystem(t *testing.T) {
	tools := listTools(t, allPluginsApp(t).Handler(), "threecx")
	if len(tools) == 0 {
		t.Fatal("threecx advertised no tools")
	}

	// The property bodies are read loosely on purpose: a nullable field's
	// "type" is an array rather than a string, and this test is about which
	// properties exist, not about how each one is spelt.
	type property struct {
		Type        json.RawMessage `json:"type"`
		Description string          `json:"description"`
	}
	type schema struct {
		Properties map[string]property `json:"properties"`
		Required   []string            `json:"required"`
	}
	read := func(raw json.RawMessage, what, tool string) schema {
		t.Helper()
		var s schema
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatalf("%s %s schema: %v", tool, what, err)
		}
		return s
	}

	var withCustomer int
	for _, tl := range tools {
		in := read(tl.InputSchema, "input", tl.Name)
		out := read(tl.OutputSchema, "output", tl.Name)

		if tl.Name == "threecx_list_customers" {
			// Discovery is the one that carries the relationship rather than
			// naming one side of it.
			if _, ok := out.Properties["customers"]; !ok {
				t.Errorf("%s should return customers", tl.Name)
			}
			if !strings.Contains(string(tl.OutputSchema), `"systems"`) ||
				!strings.Contains(string(tl.OutputSchema), `"system_count"`) {
				t.Errorf("%s must advertise each customer's phone systems, got %s",
					tl.Name, tl.OutputSchema)
			}
			continue
		}

		cust, takesCustomer := in.Properties["customer"]
		if !takesCustomer {
			continue
		}
		withCustomer++

		sys, takesSystem := in.Properties["system"]
		if !takesSystem {
			t.Errorf("%s takes a customer but no system, so it cannot be pointed at "+
				"the second of a business's phone systems", tl.Name)
			continue
		}
		if string(sys.Type) != `"string"` || string(cust.Type) != `"string"` {
			t.Errorf("%s: customer and system should both be strings, got %s and %s",
				tl.Name, cust.Type, sys.Type)
		}
		// Neither is required, which is what keeps a single-system instance
		// callable with no arguments at all -- and what a client written
		// before any of this kept doing.
		for _, req := range in.Required {
			if req == "customer" || req == "system" {
				t.Errorf("%s makes %q required; a business with one phone system "+
					"must stay callable without it", tl.Name, req)
			}
		}
		if sys.Description == "" {
			t.Errorf("%s: the system argument says nothing about when to use it", tl.Name)
		}
		// The description has to tell a model where the value comes from, or
		// it will invent one.
		if !strings.Contains(sys.Description, "list_customers") {
			t.Errorf("%s: the system argument should point at list_customers, got %q",
				tl.Name, sys.Description)
		}

		// And the answer says which phone system it describes.
		//
		// The type is checked, not only the presence. A result struct with a
		// `system` field of its own shadows the embedded one in Go's JSON --
		// silently, because the outer field wins on depth -- and the answer
		// then loses the name while still carrying `customer` and
		// `system_id`. Two result types had exactly that field, and the only
		// thing that would have caught it is this line.
		for _, field := range []string{"customer", "system", "system_id"} {
			prop, ok := out.Properties[field]
			if !ok {
				t.Errorf("%s does not name %q on its answer, so a result about one of a "+
					"customer's phone systems cannot be told from another's", tl.Name, field)
				continue
			}
			if string(prop.Type) != `"string"` {
				t.Errorf("%s answers with a %q of type %s; the source fields are strings, "+
					"so something in that result is shadowing one of them",
					tl.Name, field, prop.Type)
			}
		}
	}

	if withCustomer < 20 {
		t.Fatalf("only %d tools take a customer; the walk found too few to be "+
			"checking what it thinks it is", withCustomer)
	}

	// Nothing a client is handed names a credential. The tool list is the one
	// thing every caller gets without asking for it.
	for _, tl := range tools {
		blob := strings.ToLower(string(tl.InputSchema) + string(tl.OutputSchema) + tl.Description)
		for _, word := range []string{"password", "secret", "api key", "token"} {
			if strings.Contains(blob, word) {
				t.Errorf("%s mentions %q in what it advertises", tl.Name, word)
			}
		}
	}
}
