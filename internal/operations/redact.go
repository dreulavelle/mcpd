package operations

import (
	"regexp"
	"strings"
)

// Error detail from an upstream call ends up in the audit trail and the admin
// interface, both more widely readable than the process logs. An HTTP client
// error can easily quote a request URL carrying a token or echo an
// Authorization header, so detail is filtered before it is persisted.
//
// This is a backstop rather than a licence: plugin error messages should not
// contain credentials in the first place.

// redactRule pairs a pattern with the submatch index to preserve. Everything
// the pattern matched beyond that group is replaced.
//
// Using an explicit keep-group rather than inferring a separator is what makes
// this reliable: a header like "Authorization: Bearer abc" has two plausible
// separators, and guessing between them is how a token survives redaction.
type redactRule struct {
	re   *regexp.Regexp
	keep int
}

var redactRules = []redactRule{
	// Authorization-style headers, consuming the whole value to end of line.
	{regexp.MustCompile(`(?i)((?:proxy-)?authorization\s*[:=]\s*)[^\r\n]*`), 1},
	// Bearer or Basic credentials wherever they appear.
	{regexp.MustCompile(`(?i)\b((?:bearer|basic)\s+)[A-Za-z0-9._~+/=-]{8,}`), 1},
	// Query parameters naming a credential.
	{regexp.MustCompile(`(?i)([?&](?:access_token|refresh_token|token|api_?key|client_secret|password|secret|code|code_verifier)=)[^&\s]*`), 1},
	// JSON fields naming a credential.
	{regexp.MustCompile(`(?i)("(?:access_token|refresh_token|token|api_?key|client_secret|password|secret)"\s*:\s*")[^"]*(")`), 1},
	// userinfo embedded in a URL.
	{regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^/\s:@]+:)[^/\s@]+(@)`), 1},
}

// redactionMarker replaces a removed value.
const redactionMarker = "[REDACTED]"

// maxDetailLen bounds persisted error detail. An upstream returning an HTML
// error page would otherwise put a kilobyte of markup into every audit record.
const maxDetailLen = 1024

// redact removes credential-shaped substrings and bounds the result.
func redact(s string) string {
	if s == "" {
		return ""
	}
	out := s
	for _, rule := range redactRules {
		out = replaceKeeping(rule, out)
	}
	if len(out) > maxDetailLen {
		out = out[:maxDetailLen] + "… (truncated)"
	}
	return out
}

// replaceKeeping rewrites every match, preserving the named capture group and
// the trailing group when the rule has one, and replacing the value between.
func replaceKeeping(rule redactRule, s string) string {
	matches := rule.re.FindAllStringSubmatchIndex(s, -1)
	if matches == nil {
		return s
	}

	var b strings.Builder
	last := 0
	for _, m := range matches {
		// m[0]:m[1] is the whole match; m[2i]:m[2i+1] is group i.
		b.WriteString(s[last:m[0]])

		keepStart, keepEnd := m[2*rule.keep], m[2*rule.keep+1]
		if keepStart >= 0 {
			b.WriteString(s[keepStart:keepEnd])
		}
		b.WriteString(redactionMarker)

		// A trailing group (a closing quote, an "@") is preserved so the
		// message stays syntactically recognisable.
		if len(m) >= 6 {
			if tailStart, tailEnd := m[4], m[5]; tailStart >= 0 {
				b.WriteString(s[tailStart:tailEnd])
			}
		}
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String()
}
