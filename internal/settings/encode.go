package settings

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Encode turns the string a form submits into the value stored in the table.
//
// One encoder rather than one per caller. Values arrive from the dashboard,
// and once from the configuration file an upgrade imports; storing the two in
// different shapes would make a value read back differently depending on where
// it came from, which is the kind of difference nobody looks for.
//
// Secrets are not encoded. They are stored as ciphertext over the bytes given.
func Encode(kind Kind, value string) (string, error) {
	switch kind {
	case KindBool:
		b, err := strconv.ParseBool(value)
		if err != nil {
			return "", fmt.Errorf("expected true or false")
		}
		return strconv.FormatBool(b), nil

	case KindInt, KindDuration:
		n, err := strconv.Atoi(value)
		if err != nil {
			return "", fmt.Errorf("expected a whole number")
		}
		return strconv.Itoa(n), nil

	case KindList:
		items := []string{}
		for _, part := range strings.Split(value, ",") {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				items = append(items, trimmed)
			}
		}
		encoded, err := json.Marshal(items)
		if err != nil {
			return "", err
		}
		return string(encoded), nil

	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	}
}
