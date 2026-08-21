package config

import (
	"testing"
)

// Plaintext on a private network is a warning, not an error. The rule that
// made it an error existed for the OAuth issuer contract -- RFC 8414 requires
// an https issuer -- and mcpd is no longer an authorization server.
func TestPlaintextPublicURLIsAccepted(t *testing.T) {
	cfg := validConfig()
	cfg.Server.PublicURL = "http://192.168.50.125:9080"

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// Serving https while advertising http would send every client to a port that
// cannot answer them.
func TestATLSListenerNeedsAnHTTPSPublicURL(t *testing.T) {
	cfg := validConfig()
	cfg.Server.TLS = TLS{Mode: "self-signed"}
	cfg.Server.PublicURL = "http://192.168.50.125:9080"

	if err := cfg.Validate(); err == nil {
		t.Fatal("an https listener advertised as http must be refused")
	}
}
