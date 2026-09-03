package auth

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxLabelRunes bounds a name -- a group's, a role's, a key's -- in runes
// rather than bytes so that a name in a script whose characters cost three
// bytes each is not a third as long as one in ASCII. The schema enforces the
// same bound in the same units.
const MaxLabelRunes = 64

// ValidateLabel checks and normalises a name somebody will read.
//
// The same three rules a display name is held to, for the same reasons: it is
// rendered in a list, it appears in the audit trail, and a bidirectional
// override or a newline in it makes it read as something it is not. It is
// deliberately not a slug -- an operator names a group after the team it is
// for, and refusing spaces or capitals would mean asking them to spell it in
// a way nobody says out loud.
//
// pkg and noun go into the error so that a message about a key's name does
// not send somebody to the Groups page.
func ValidateLabel(pkg, noun, raw string) (string, error) {
	return ValidateText(pkg, noun, raw, MaxLabelRunes)
}

// ValidateText is ValidateLabel with the length supplied, for a description
// that may run longer than a name.
func ValidateText(pkg, noun, raw string, maxRunes int) (string, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", fmt.Errorf("%s: a %s is required", pkg, noun)
	}
	if !utf8.ValidString(text) {
		return "", fmt.Errorf("%s: a %s must be valid UTF-8", pkg, noun)
	}
	for _, r := range text {
		switch {
		case unicode.IsControl(r):
			return "", fmt.Errorf("%s: a %s cannot contain control characters", pkg, noun)
		case unicode.Is(unicode.Cf, r):
			return "", fmt.Errorf("%s: a %s cannot contain invisible formatting characters", pkg, noun)
		}
	}
	if utf8.RuneCountInString(text) > maxRunes {
		return "", fmt.Errorf("%s: a %s must be at most %d characters", pkg, noun, maxRunes)
	}
	return text, nil
}
