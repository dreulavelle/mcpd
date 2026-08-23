package registry

import (
	"encoding/json"
	"slices"
	"testing"
	"time"
)

// The dashboard is built separately against these key names, so a rename here
// is a broken page over there with nothing in Go to catch it. This pins the
// wire shape of both catalogue responses.
func TestWireShape(t *testing.T) {
	entry := Entry{
		Name: "io.github.example/weather", SuggestedName: "weather",
		Title: "Weather", Description: "Reads the forecast.", Version: "1.2.0",
		Transport: "streamable-http", URL: "https://weather.example/mcp",
		UpdatedAt: time.Date(2026, 4, 13, 17, 32, 20, 0, time.UTC),
		Addable:   true, Reason: "",
	}

	page := Page{
		Source: officialSource, Entries: []Entry{entry},
		NextCursor: "io.github.example/weather:1.2.0",
		Stale:      false, RetrievedAt: time.Now().UTC(),
	}
	assertKeys(t, "page", page,
		"source", "entries", "next_cursor", "stale", "retrieved_at")

	var pageBody struct {
		Entries []json.RawMessage `json:"entries"`
	}
	encode(t, page, &pageBody)
	assertRawKeys(t, "entry", pageBody.Entries[0],
		"name", "suggested_name", "title", "description", "version",
		"transport", "url", "updated_at", "addable")

	// Detail carries the entry's own fields flat, plus the document. The
	// document is what the import endpoint is posted, so it has to be an
	// object rather than a string.
	detail := Detail{
		Entry: entry, Document: json.RawMessage(`{"name":"io.github.example/weather"}`),
		Source: officialSource, Stale: true, RetrievedAt: time.Now().UTC(),
	}
	assertKeys(t, "detail", detail,
		"name", "suggested_name", "title", "description", "version",
		"transport", "url", "updated_at", "addable",
		"document", "source", "stale", "retrieved_at")

	var withDoc struct {
		Document map[string]any `json:"document"`
	}
	encode(t, detail, &withDoc)
	if withDoc.Document["name"] != "io.github.example/weather" {
		t.Errorf("document = %v, want the server.json as an object", withDoc.Document)
	}

	// A reason only appears when there is one, so an addable entry does not
	// carry an empty field for the page to test.
	unavailable := entry
	unavailable.Addable, unavailable.Reason, unavailable.Transport, unavailable.URL =
		false, "declares no remotes", "", ""
	assertKeys(t, "unavailable entry", unavailable,
		"name", "suggested_name", "title", "description", "version",
		"updated_at", "addable", "reason")
}

func encode(t *testing.T, v, into any) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatal(err)
	}
}

func assertKeys(t *testing.T, what string, v any, want ...string) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	assertRawKeys(t, what, raw, want...)
}

func assertRawKeys(t *testing.T, what string, raw json.RawMessage, want ...string) {
	t.Helper()
	var got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range want {
		if _, ok := got[key]; !ok {
			t.Errorf("%s is missing %q; keys are %v", what, key, keysOf(got))
		}
	}
	for key := range got {
		if !slices.Contains(want, key) {
			t.Errorf("%s carries an unexpected key %q", what, key)
		}
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
