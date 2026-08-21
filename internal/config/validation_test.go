package config

import (
	"strings"
	"testing"
)

// The failure this prevents is unusually hard to read: ChatGPT fetches the
// metadata successfully and then rejects it, reporting only that the server
// "does not implement OAuth". RFC 8414 requires an https issuer.
func TestOAuthOverPlaintextIsRefused(t *testing.T) {
	for _, mode := range []string{"oauth", "mixed"} {
		cfg := validConfig()
		cfg.Auth.Mode = mode
		cfg.Server.PublicURL = "http://192.168.50.125:9080"

		err := cfg.Validate()
		if err == nil {
			t.Fatalf("auth.mode %q over http must be refused", mode)
		}
		if !strings.Contains(err.Error(), "https") {
			t.Fatalf("error = %v, want it to name the requirement", err)
		}
	}
}

func TestOAuthOverHTTPSIsAccepted(t *testing.T) {
	cfg := validConfig()
	cfg.Auth.Mode = "mixed"
	cfg.Server.PublicURL = "https://192.168.50.125:9080"
	cfg.Server.TLS = TLS{Mode: "self-signed"}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// Static auth mounts no authorization server, so plaintext on a private
// network stays a warning rather than becoming an error.
func TestStaticAuthOverPlaintextStillWorks(t *testing.T) {
	cfg := validConfig()
	cfg.Auth.Mode = "static"
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
