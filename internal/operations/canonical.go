package operations

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
)

// Canonicalize rewrites arbitrary JSON into a deterministic byte form: object
// keys sorted lexicographically, no insignificant whitespace, and numbers
// emitted in their shortest round-trip representation.
//
// Determinism is the whole point. The payload hash is what makes an approved
// mutation tamper-evident, so two semantically identical payloads must produce
// byte-identical output regardless of how they were serialised on the way in.
func Canonicalize(raw json.RawMessage) ([]byte, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return []byte("null"), nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	// UseNumber preserves the literal numeric text, so 1.0 does not silently
	// become 1 and change the hash.
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("canonicalize: %w", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("canonicalize: trailing data after JSON value")
	}
	var buf bytes.Buffer
	if err := writeCanonical(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeCanonical(buf *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		buf.WriteString(strconv.FormatBool(t))
	case json.Number:
		// Normalise through float64 so that 1.50 and 1.5 agree, while integers
		// too large for float64 keep their exact literal form.
		if i, err := t.Int64(); err == nil {
			buf.WriteString(strconv.FormatInt(i, 10))
			return nil
		}
		f, err := t.Float64()
		if err != nil {
			buf.WriteString(t.String())
			return nil
		}
		buf.WriteString(strconv.FormatFloat(f, 'g', -1, 64))
	case string:
		b, err := json.Marshal(t)
		if err != nil {
			return err
		}
		buf.Write(b)
	case []any:
		buf.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonical(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			kb, err := json.Marshal(k)
			if err != nil {
				return err
			}
			buf.Write(kb)
			buf.WriteByte(':')
			if err := writeCanonical(buf, t[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return fmt.Errorf("canonicalize: unsupported type %T", v)
	}
	return nil
}

// PayloadHash derives the tamper-evidence hash for a mutation.
//
// The hash covers exactly the fields that define what will be done: the plugin,
// the action, the target, and the parameters. It deliberately excludes risk,
// impact, expiry, and the requesting principal — those are host-assigned
// metadata, and folding them in would mean a policy change invalidated stored
// operations that had not themselves changed.
//
// Length prefixes prevent field-boundary ambiguity: without them, plugin "ab" +
// action "c" and plugin "a" + action "bc" would hash identically.
func PayloadHash(plugin, action string, target, params json.RawMessage) (string, error) {
	ct, err := Canonicalize(target)
	if err != nil {
		return "", fmt.Errorf("target: %w", err)
	}
	cp, err := Canonicalize(params)
	if err != nil {
		return "", fmt.Errorf("params: %w", err)
	}
	h := sha256.New()
	for _, part := range [][]byte{[]byte(plugin), []byte(action), ct, cp} {
		fmt.Fprintf(h, "%d:", len(part))
		h.Write(part)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Recompute derives the payload hash from an operation as loaded from storage.
// The executor calls this immediately before claiming and compares the result
// against the stored hash.
func Recompute(op *Operation) (string, error) {
	if op == nil {
		return "", ErrNotFound
	}
	return PayloadHash(op.Plugin, op.Action, op.Target, op.Params)
}

// CanonicalEqual reports whether two JSON snapshots are equivalent after
// canonicalisation. A malformed snapshot compares unequal, which fails closed:
// an unreadable snapshot blocks execution rather than permitting it.
//
// Equality alone is not evidence. Two absent snapshots canonicalise to "null"
// and compare equal, which is the right answer for a mutation whose desired
// state is absence -- a delete confirmed by observing nothing -- and the wrong
// one for a drift check that had nothing to compare. Callers that need the
// difference ask Declared, or use CheckDrift.
func CanonicalEqual(a, b json.RawMessage) bool {
	ca, err := Canonicalize(a)
	if err != nil {
		return false
	}
	cb, err := Canonicalize(b)
	if err != nil {
		return false
	}
	return bytes.Equal(ca, cb)
}

// Declared reports whether a snapshot carries any state at all.
//
// Absent, JSON null, an empty object and an empty array all carry none: there
// is no field in them for a comparison to notice changing. An unreadable
// snapshot counts as declared, because it must reach CanonicalEqual and fail
// there rather than be waved through as a mutation that asked for nothing.
func Declared(raw json.RawMessage) bool {
	c, err := Canonicalize(raw)
	if err != nil {
		return true
	}
	switch string(c) {
	case "null", "{}", "[]":
		return false
	}
	return true
}

// DriftCheck is the outcome of comparing the precondition snapshot taken at
// proposal against one taken immediately before execution.
type DriftCheck int

const (
	// DriftNotChecked means the mutation declared no preconditions, so there
	// was nothing to compare. It is deliberately not the same value as
	// DriftNone: a comparison that did not happen is not a comparison that
	// passed, and recording it as one is how a check nobody performed ends up
	// presented as a check that succeeded.
	DriftNotChecked DriftCheck = iota
	// DriftNone means the target still looks the way it did at proposal.
	DriftNone
	// DriftDetected means it does not, so applying the change would overwrite
	// something nobody reviewed.
	DriftDetected
)

// String implements fmt.Stringer.
func (d DriftCheck) String() string {
	switch d {
	case DriftNone:
		return "none"
	case DriftDetected:
		return "detected"
	default:
		return "not_checked"
	}
}

// CheckDrift compares the proposed and current precondition snapshots.
//
// One snapshot declared and the other not is drift, not an absence of
// checking: the target grew or lost the state the mutation depends on.
func CheckDrift(proposed, current json.RawMessage) DriftCheck {
	if !Declared(proposed) && !Declared(current) {
		return DriftNotChecked
	}
	if !CanonicalEqual(proposed, current) {
		return DriftDetected
	}
	return DriftNone
}
