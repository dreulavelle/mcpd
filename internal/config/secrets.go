package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SecretResolver turns a secret reference from configuration into its value.
//
// References are resolved lazily and the results are not cached in any struct
// that could be logged or serialised, which is what keeps credentials out of
// config dumps and the admin API.
type SecretResolver struct {
	// credentialsDir is systemd's CREDENTIALS_DIRECTORY when running under a
	// unit with LoadCredential=. Empty when not running under systemd.
	credentialsDir string
}

// NewSecretResolver builds a resolver, picking up systemd's credentials
// directory if present.
func NewSecretResolver() *SecretResolver {
	return &SecretResolver{credentialsDir: os.Getenv("CREDENTIALS_DIRECTORY")}
}

// Resolve returns the value behind a reference.
//
// Supported forms:
//
//	env:NAME          read from the environment
//	credential:NAME   read from systemd's LoadCredential directory
//	file:/path        read from a file
//
// A bare value with no prefix is rejected. Allowing inline secrets would make
// it easy to paste a token into the config file, where it would end up in
// version control.
func (s *SecretResolver) Resolve(ref string) (string, error) {
	scheme, name, ok := strings.Cut(strings.TrimSpace(ref), ":")
	if !ok {
		return "", fmt.Errorf(
			"config: secret reference %q has no scheme; use env:, credential: or file:", ref)
	}
	if name == "" {
		return "", fmt.Errorf("config: secret reference %q names nothing", ref)
	}

	switch scheme {
	case "env":
		v, present := os.LookupEnv(name)
		if !present {
			return "", fmt.Errorf("config: environment variable %s is not set", name)
		}
		if v == "" {
			return "", fmt.Errorf("config: environment variable %s is empty", name)
		}
		return v, nil

	case "credential":
		if s.credentialsDir == "" {
			return "", fmt.Errorf(
				"config: secret %q requests a systemd credential, but "+
					"CREDENTIALS_DIRECTORY is unset; is LoadCredential= configured?", ref)
		}
		// Reject traversal: a credential name is a filename, not a path.
		if strings.ContainsAny(name, `/\`) || name == ".." {
			return "", fmt.Errorf("config: credential name %q must not contain a path", name)
		}
		return readSecretFile(filepath.Join(s.credentialsDir, name))

	case "file":
		if !filepath.IsAbs(name) {
			return "", fmt.Errorf("config: secret file %q must be an absolute path", name)
		}
		return readSecretFile(name)

	default:
		return "", fmt.Errorf("config: unknown secret scheme %q in %q", scheme, ref)
	}
}

// readSecretFile reads a secret from disk, trimming the trailing newline that
// editors and `echo` add.
func readSecretFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		// The path is included because it is not itself secret and an operator
		// needs it to diagnose; the contents never are.
		return "", fmt.Errorf("config: read secret from %s: %w", path, err)
	}
	v := strings.TrimRight(string(b), "\r\n")
	if v == "" {
		return "", fmt.Errorf("config: secret file %s is empty", path)
	}
	return v, nil
}
