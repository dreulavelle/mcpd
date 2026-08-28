package app

import (
	"strings"
	"testing"
)

const wordpress = `{
  "mcpServers": {
    "spark-wordpress-staging": {
      "command": "npx",
      "args": ["-y", "@automattic/mcp-wordpress-remote@latest"],
      "env": {
        "WP_API_URL": "https://example.test/wp-json/mcp/mcp-adapter-default-server",
        "WP_API_USERNAME": "spark_mcp",
        "WP_API_PASSWORD": "hunter2"
      }
    }
  }
}`

// TestClientConfigDocument_OneServerNeedsNoChoice: the common paste is a file
// with a single server in it, and asking which one would be asking about a
// list of one.
func TestClientConfigDocument_OneServerNeedsNoChoice(t *testing.T) {
	got, err := clientConfigDocument("anything", []byte(
		`{"mcpServers": {"example": {"url": "https://a.test/mcp"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "https://a.test/mcp") {
		t.Errorf("wrong document: %s", got)
	}
}

// TestClientConfigDocument_SeveralServersUseTheName: an import records one
// server, so the name on the form selects which.
func TestClientConfigDocument_SeveralServersUseTheName(t *testing.T) {
	raw := []byte(`{"mcpServers": {
	  "alpha": {"url": "https://alpha.test/mcp"},
	  "beta":  {"url": "https://beta.test/mcp"}
	}}`)

	got, err := clientConfigDocument("beta", raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "beta.test") {
		t.Errorf("selected the wrong entry: %s", got)
	}

	// Without a name that matches, the refusal lists what is on offer rather
	// than picking for them.
	_, err = clientConfigDocument("neither", raw)
	if err == nil {
		t.Fatal("an ambiguous paste must not be resolved by guessing")
	}
	for _, want := range []string{`"alpha"`, `"beta"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the choice does not name %s: %v", want, err)
		}
	}
}

// TestClientConfigDocument_ExplainsAStdioOnlyFile defends the message an
// operator gets for the three-quarters case: a file whose servers all run
// local commands. It has to name them and say why, not report an empty result.
func TestClientConfigDocument_ExplainsAStdioOnlyFile(t *testing.T) {
	_, err := clientConfigDocument("spark-wordpress-staging", []byte(wordpress))
	if err == nil {
		t.Fatal("a command entry must not be imported")
	}
	msg := err.Error()
	if !strings.Contains(msg, "npx -y @automattic/mcp-wordpress-remote@latest") {
		t.Errorf("does not name the command: %s", msg)
	}
	// And it points at the server behind the wrapper, which is reachable.
	if !strings.Contains(msg, "https://example.test/wp-json/mcp/mcp-adapter-default-server") {
		t.Errorf("does not offer the remote behind the shim: %s", msg)
	}
	// The password in that file is never quoted back.
	if strings.Contains(msg, "hunter2") {
		t.Error("the error repeated a credential from the pasted file")
	}
}
