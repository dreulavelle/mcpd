package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseEnv(t *testing.T) {
	input := `
# a comment
KEY=value
export EXPORTED=yes
QUOTED="double quoted"
SINGLE='single quoted'
EMPTY=
SPACES = padded
WITH_COMMENT=value # trailing note
HASH_IN_QUOTES="value#not-a-comment"
ESCAPED="line\nbreak"
EQUALS_IN_VALUE=a=b=c
BASE64_TOKEN=dG9rZW4rd2l0aC9zbGFzaGVzPT0=
`
	got, err := parseEnv(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"KEY":             "value",
		"EXPORTED":        "yes",
		"QUOTED":          "double quoted",
		"SINGLE":          "single quoted",
		"EMPTY":           "",
		"SPACES":          "padded",
		"WITH_COMMENT":    "value",
		"HASH_IN_QUOTES":  "value#not-a-comment",
		"ESCAPED":         "line\nbreak",
		"EQUALS_IN_VALUE": "a=b=c",
		"BASE64_TOKEN":    "dG9rZW4rd2l0aC9zbGFzaGVzPT0=",
	}
	for k, expected := range want {
		if got[k] != expected {
			t.Errorf("%s = %q, want %q", k, got[k], expected)
		}
	}
	if len(got) != len(want) {
		t.Errorf("parsed %d vars, want %d: %v", len(got), len(want), got)
	}
}

// A generated token often ends in '=' padding or contains '/' and '+'.
// Mangling one produces an authentication failure with no obvious cause.
func TestParseEnv_PreservesTokenShapedValues(t *testing.T) {
	tokens := []string{
		"dG7PnM2j7eerpqcDV6zFM9X8LGOD7dU6/Hs64WBpzMU=",
		"sk-proj-abc123_XYZ-456",
		"a+b/c==",
	}
	for _, token := range tokens {
		got, err := parseEnv(strings.NewReader("TOKEN=" + token))
		if err != nil {
			t.Fatal(err)
		}
		if got["TOKEN"] != token {
			t.Errorf("TOKEN = %q, want %q", got["TOKEN"], token)
		}
	}
}

func TestParseEnv_Rejects(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no equals", "JUST_A_WORD", "not KEY=VALUE"},
		{"invalid name", "not-a-name=x", "invalid name"},
		{"leading digit", "1KEY=x", "invalid name"},
		{"unterminated double quote", `KEY="open`, "unterminated double quote"},
		{"unterminated single quote", "KEY='open", "unterminated single quote"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseEnv(strings.NewReader(tc.input))
			if err == nil {
				t.Fatalf("expected %q to be rejected", tc.input)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// A parse failure on a credential line must not print the credential.
func TestParseEnv_ErrorsDoNotQuoteLongValues(t *testing.T) {
	secret := strings.Repeat("s3cr3t", 20)
	_, err := parseEnv(strings.NewReader(secret))
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("the error quoted the whole line: %v", err)
	}
}

// The real environment must win. An orchestrator injecting a rotated secret
// cannot be silently overridden by a stale value baked into an image.
func TestLoadEnvFile_RealEnvironmentWins(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "FROM_FILE=file-value\nOVERRIDDEN=file-value\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("OVERRIDDEN", "real-value")

	if err := LoadEnvFile(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("FROM_FILE"); got != "file-value" {
		t.Fatalf("FROM_FILE = %q, want the file's value", got)
	}
	if got := os.Getenv("OVERRIDDEN"); got != "real-value" {
		t.Fatalf("OVERRIDDEN = %q; the real environment must take precedence", got)
	}
	os.Unsetenv("FROM_FILE")
}

// A missing .env is normal: systemd passes credentials through
// LoadCredential instead, and both paths must work.
func TestLoadEnvFile_MissingFileIsNotAnError(t *testing.T) {
	if err := LoadEnvFile(filepath.Join(t.TempDir(), "absent")); err != nil {
		t.Fatalf("a missing .env should not be an error: %v", err)
	}
}

func TestLoadEnvFile_ReportsMalformedContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("BROKEN LINE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadEnvFile(path); err == nil {
		t.Fatal("a malformed .env must be reported, not silently ignored")
	}
}

func TestUnquote(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"plain", "plain"},
		{`"quoted"`, "quoted"},
		{"'quoted'", "quoted"},
		{`"with \"escaped\" quotes"`, `with "escaped" quotes`},
		{`'literal \n stays'`, `literal \n stays`},
		{"trailing # comment", "trailing"},
		{`"# not a comment"`, "# not a comment"},
		{`"unknown \d escape"`, `unknown \d escape`},
	}
	for _, tc := range tests {
		got, err := unquote(tc.in)
		if err != nil {
			t.Errorf("unquote(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("unquote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
