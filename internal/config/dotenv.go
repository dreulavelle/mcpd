package config

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// DefaultEnvFile is the conventional name, looked for beside the binary's
// working directory.
const DefaultEnvFile = ".env"

// LoadEnvFile reads a dotenv file into the process environment.
//
// A missing file is not an error: a .env is a convenience for local and
// container deployments, while a systemd unit passes credentials through
// LoadCredential instead. Both must work.
//
// Existing environment variables always win. That ordering matters in
// production: an orchestrator injecting a rotated secret must not be silently
// overridden by a stale value in a file baked into an image.
func LoadEnvFile(path string) error {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("config: open %s: %w", path, err)
	}
	defer f.Close()

	// A .env holds credentials. World-readable permissions are worth saying
	// out loud rather than silently accepting.
	if info, statErr := f.Stat(); statErr == nil && info.Mode().Perm()&0o044 != 0 {
		fmt.Fprintf(os.Stderr,
			"warning: %s is readable by other users (mode %04o); it holds credentials, "+
				"so consider chmod 600\n", path, info.Mode().Perm())
	}

	vars, err := parseEnv(f)
	if err != nil {
		return fmt.Errorf("config: parse %s: %w", path, err)
	}

	for key, value := range vars {
		if _, present := os.LookupEnv(key); present {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("config: set %s: %w", key, err)
		}
	}
	return nil
}

// parseEnv reads KEY=VALUE lines.
//
// The dialect is deliberately the common subset every dotenv implementation
// agrees on: comments, blank lines, an optional export prefix, and single or
// double quotes. It does not do variable interpolation. A .env that expands
// $OTHER means something different depending on which tool reads it, and a
// credential file is the worst place for that ambiguity.
func parseEnv(r io.Reader) (map[string]string, error) {
	out := make(map[string]string)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)

	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")

		key, value, found := strings.Cut(line, "=")
		if !found {
			return nil, fmt.Errorf("line %d is not KEY=VALUE: %q", lineNo, truncate(line))
		}

		key = strings.TrimSpace(key)
		if !validEnvKey(key) {
			return nil, fmt.Errorf("line %d has an invalid name %q; "+
				"names must match [A-Za-z_][A-Za-z0-9_]*", lineNo, key)
		}

		unquoted, err := unquote(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		out[key] = unquoted
	}
	return out, scanner.Err()
}

// unquote strips matching quotes and resolves escapes inside double quotes.
//
// An unquoted value keeps everything up to a trailing comment, which is what
// makes `KEY=value # note` behave as people expect. Inside quotes a # is
// literal, so a token containing one survives.
func unquote(v string) (string, error) {
	if v == "" {
		return "", nil
	}

	switch v[0] {
	case '"':
		closing := findClosing(v, '"')
		if closing < 0 {
			return "", fmt.Errorf("unterminated double quote")
		}
		return expandEscapes(v[1:closing]), nil

	case '\'':
		// Single quotes are literal, including backslashes.
		closing := strings.IndexByte(v[1:], '\'')
		if closing < 0 {
			return "", fmt.Errorf("unterminated single quote")
		}
		return v[1 : closing+1], nil

	default:
		if idx := strings.Index(v, " #"); idx >= 0 {
			v = v[:idx]
		}
		return strings.TrimSpace(v), nil
	}
}

// findClosing locates the closing quote, skipping escaped ones.
func findClosing(s string, quote byte) int {
	for i := 1; i < len(s); i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		if s[i] == quote {
			return i
		}
	}
	return -1
}

// expandEscapes resolves the escapes double quotes are expected to honour.
func expandEscapes(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '\\', '"', '\'':
			b.WriteByte(s[i])
		default:
			// Preserve an unrecognised escape rather than swallowing the
			// backslash: a secret containing \d must survive intact.
			b.WriteByte('\\')
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

func validEnvKey(k string) bool {
	if k == "" {
		return false
	}
	for i, r := range k {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// truncate bounds a value quoted in an error. A parse failure on a credential
// line must not print the credential.
func truncate(s string) string {
	const max = 24
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
