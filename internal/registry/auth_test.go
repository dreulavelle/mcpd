package registry

import "testing"

// TestAuthFromDocument defends the distinction a marketplace row has to make
// between a server that needs no credential and one whose document never said.
//
// It exists for a bug: describe reported every document with no secret input
// as AuthNone, so an entry that declares nothing at all -- the shape roughly a
// third of the published remote servers take -- was offered as "No credential"
// and answered 401 on the first dial, after the import, with nothing naming
// the cause.
func TestAuthFromDocument(t *testing.T) {
	const head = `{"$schema":"https://static.modelcontextprotocol.io/schemas/2025-12-11/server.schema.json",
		"name":"com.example/thing","description":"A thing.","version":"1.0.0","remotes":[{"type":"streamable-http",
		"url":"https://example.test/mcp"`

	for _, tc := range []struct {
		name     string
		remote   string
		want     string
		wantAuth string
	}{
		{
			name:   "a document that names a secret asks for one",
			remote: `,"headers":[{"name":"Authorization","isSecret":true,"isRequired":true}]`,
			want:   AuthAPIKey,
		},
		{
			// Examined and found to need nothing secret. That is a finding.
			name:   "a document that declares a plain header has been examined",
			remote: `,"headers":[{"name":"X-Tenant","value":"acme"}]`,
			want:   AuthNone,
		},
		{
			name:   "a document that declares a variable has been examined",
			remote: `,"variables":{"region":{"default":"us"}}`,
			want:   AuthNone,
		},
		{
			// Silence. Not a finding, and must not be reported as one.
			name:   "a document that declares nothing says nothing",
			remote: ``,
			want:   AuthUnknown,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, addable, reason, auth := describe([]byte(head + tc.remote + `}]}`))
			if !addable {
				t.Fatalf("addable = false, reason %q", reason)
			}
			if auth != tc.want {
				t.Errorf("auth = %q, want %q", auth, tc.want)
			}
		})
	}
}
