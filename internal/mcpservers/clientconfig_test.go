package mcpservers

import (
	"strings"
	"testing"
)

// TestParseClientConfig_ConvertsRemoteEntries defends the import path for the
// file people actually have: the mcpServers config their editor already keeps.
func TestParseClientConfig_ConvertsRemoteEntries(t *testing.T) {
	got, err := ParseClientConfig([]byte(`{
	  "mcpServers": {
	    "example": {
	      "type": "http",
	      "url": "https://api.example.test/mcp",
	      "headers": {"X-Tenant": "acme", "Authorization": "Bearer sk-live-abc123"}
	    }
	  }
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Document == nil {
		t.Fatalf("got %+v", got)
	}

	doc, err := Parse(got[0].Document)
	if err != nil {
		t.Fatalf("the converted document must survive the real import path: %v", err)
	}
	remote, err := doc.Remote()
	if err != nil {
		t.Fatal(err)
	}
	if remote.URL != "https://api.example.test/mcp" {
		t.Errorf("url = %q", remote.URL)
	}

	byName := map[string]KeyValueInput{}
	for _, h := range remote.Headers {
		byName[h.Name] = h
	}
	// The credential is asked for, never carried: it came out of a file that
	// holds live keys, and a document is neither encrypted nor redacted.
	auth, ok := byName["Authorization"]
	if !ok {
		t.Fatal("the Authorization header was dropped entirely")
	}
	if !auth.Input.IsSecret {
		t.Error("Authorization must be marked secret")
	}
	if auth.Input.Value != "" || auth.Input.Default != "" {
		t.Errorf("the pasted credential was copied into the document: %q/%q",
			auth.Input.Value, auth.Input.Default)
	}
	if strings.Contains(string(got[0].Document), "sk-live-abc123") {
		t.Error("the credential survived somewhere in the document")
	}
	// A plain header keeps its value, as something to see and change.
	if byName["X-Tenant"].Input.Default != "acme" {
		t.Errorf("X-Tenant default = %q, want acme", byName["X-Tenant"].Input.Default)
	}
}

// TestParseClientConfig_RefusesLocalCommands states the boundary: this host
// serves remote servers and does not run processes.
func TestParseClientConfig_RefusesLocalCommands(t *testing.T) {
	got, err := ParseClientConfig([]byte(`{
	  "mcpServers": {"fs": {"command": "npx", "args": ["-y", "@mcp/server-filesystem"]}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Document != nil {
		t.Fatal("a command entry must not be converted")
	}
	// The command is named, because a refusal that does not say what it
	// refused sends somebody back to the file to guess.
	if !strings.Contains(got[0].Reason, "npx -y @mcp/server-filesystem") {
		t.Errorf("reason does not name the command: %s", got[0].Reason)
	}
}

// TestParseClientConfig_PointsAtTheRemoteBehindAShim covers the minority of
// stdio entries that are a wrapper in front of an HTTP server. Those are
// reachable directly, and saying so is the difference between a dead end and a
// working server.
func TestParseClientConfig_PointsAtTheRemoteBehindAShim(t *testing.T) {
	got, err := ParseClientConfig([]byte(`{
	  "mcpServers": {"wp": {
	    "command": "npx",
	    "args": ["-y", "@automattic/mcp-wordpress-remote@latest"],
	    "env": {"WP_API_URL": "https://example.test/wp-json/mcp/server",
	            "WP_API_USERNAME": "someone"}
	  }}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got[0].Suggestion, "https://example.test/wp-json/mcp/server") {
		t.Errorf("no direct address offered: %q", got[0].Suggestion)
	}
}

// TestParseClientConfig_InfersTransport defends the field that is missing two
// thirds of the time and spelled six ways when it is not.
func TestParseClientConfig_InfersTransport(t *testing.T) {
	for _, tc := range []struct {
		name, entry string
		convert     bool
	}{
		{"no type at all, but a url", `{"url": "https://a.test/mcp"}`, true},
		{"streamableHttp", `{"type": "streamableHttp", "url": "https://a.test/mcp"}`, true},
		{"streamable-http", `{"type": "streamable-http", "url": "https://a.test/mcp"}`, true},
		{"remote", `{"type": "remote", "url": "https://a.test/mcp"}`, true},
		{"a spelling nobody has published", `{"type": "wat", "url": "https://a.test/mcp"}`, true},
		{"no type at all, but a command", `{"command": "node", "args": ["x.js"]}`, false},
		{"sse is a transport this host does not serve", `{"type": "sse", "url": "https://a.test/sse"}`, false},
		{"neither a url nor a command", `{"type": "http"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseClientConfig([]byte(`{"mcpServers": {"x": ` + tc.entry + `}}`))
			if err != nil {
				t.Fatal(err)
			}
			if converted := got[0].Document != nil; converted != tc.convert {
				t.Errorf("converted = %v, want %v (reason %q)",
					converted, tc.convert, got[0].Reason)
			}
		})
	}
}

// TestParseClientConfig_ReadsJSONC exists because VS Code's mcp.json is JSONC
// by design, and a file a client accepts must not be refused here for a comma.
func TestParseClientConfig_ReadsJSONC(t *testing.T) {
	got, err := ParseClientConfig([]byte("\xef\xbb\xbf{\n" +
		"  // the server we use\n" +
		"  \"servers\": {\n" +
		"    \"example\": {\n" +
		"      /* over http */\n" +
		"      \"url\": \"https://a.test/mcp\",\n" +
		"    },\n" +
		"  }\n" +
		"}\n"))
	if err != nil {
		t.Fatalf("a BOM, comments and trailing commas must all be read: %v", err)
	}
	if len(got) != 1 || got[0].Document == nil {
		t.Fatalf("got %+v", got)
	}
}

// TestStripJSONC_LeavesStringsAlone: a // inside a URL is not a comment.
func TestStripJSONC_LeavesStringsAlone(t *testing.T) {
	got, err := ParseClientConfig([]byte(
		`{"mcpServers": {"x": {"url": "https://a.test/mcp", "headers": {"X-Note": "a/*b*/c"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Parse(got[0].Document)
	if err != nil {
		t.Fatal(err)
	}
	remote, _ := doc.Remote()
	if remote.URL != "https://a.test/mcp" {
		t.Errorf("the // in the URL was eaten: %q", remote.URL)
	}
	for _, h := range remote.Headers {
		if h.Name == "X-Note" && h.Input.Default != "a/*b*/c" {
			t.Errorf("a comment marker inside a string was stripped: %q", h.Input.Default)
		}
	}
}

// TestParseClientConfig_ReportsEveryEntry: an operator pasting a file with
// four servers needs to see what became of all four.
func TestParseClientConfig_ReportsEveryEntry(t *testing.T) {
	got, err := ParseClientConfig([]byte(`{"mcpServers": {
	  "b-remote": {"url": "https://b.test/mcp"},
	  "a-local":  {"command": "node"},
	  "c-remote": {"url": "https://c.test/mcp"}
	}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d entries, want 3", len(got))
	}
	// Sorted, so the list is stable between pastes of the same file.
	if got[0].Name != "a-local" || got[1].Name != "b-remote" || got[2].Name != "c-remote" {
		t.Errorf("out of order: %s, %s, %s", got[0].Name, got[1].Name, got[2].Name)
	}
}

// TestLooksLikeClientConfig tells the two pasteable files apart, so a wrong
// paste is named rather than reported as a schema this host cannot read.
func TestLooksLikeClientConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want bool
	}{
		{"a claude desktop config", `{"mcpServers": {"x": {"url": "https://a.test"}}}`, true},
		{"a vs code config", `{"servers": {"x": {"url": "https://a.test"}}}`, true},
		{"a server.json", `{"$schema": "` + SchemaURI + `", "name": "a/b", "remotes": []}`, false},
		{"an empty object", `{}`, false},
		{"not json at all", `hello`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := LooksLikeClientConfig([]byte(tc.raw)); got != tc.want {
				t.Errorf("= %v, want %v", got, tc.want)
			}
		})
	}
}
