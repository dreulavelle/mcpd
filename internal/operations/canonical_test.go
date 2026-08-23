package operations

import (
	"encoding/json"
	"testing"
)

func TestCanonicalize_DeterministicAcrossKeyOrder(t *testing.T) {
	a := json.RawMessage(`{"b":2,"a":1,"c":{"z":true,"y":[3,1,2]}}`)
	b := json.RawMessage(`{"c":{"y":[3,1,2],"z":true},"a":1,"b":2}`)

	ca, err := Canonicalize(a)
	if err != nil {
		t.Fatal(err)
	}
	cb, err := Canonicalize(b)
	if err != nil {
		t.Fatal(err)
	}
	if string(ca) != string(cb) {
		t.Fatalf("key order changed the canonical form:\n a=%s\n b=%s", ca, cb)
	}
	if want := `{"a":1,"b":2,"c":{"y":[3,1,2],"z":true}}`; string(ca) != want {
		t.Fatalf("canonical form = %s, want %s", ca, want)
	}
}

// Array order is semantically meaningful and must NOT be normalised away.
func TestCanonicalize_PreservesArrayOrder(t *testing.T) {
	a, _ := Canonicalize(json.RawMessage(`[1,2,3]`))
	b, _ := Canonicalize(json.RawMessage(`[3,2,1]`))
	if string(a) == string(b) {
		t.Fatal("array order must be significant")
	}
}

func TestCanonicalize_WhitespaceAndNumberFormatting(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"whitespace stripped", "{ \"a\" : 1 ,\n\"b\":2 }", `{"a":1,"b":2}`},
		{"float normalised", `{"a":1.50}`, `{"a":1.5}`},
		{"integer stays integral", `{"a":1}`, `{"a":1}`},
		{"empty input is null", ``, `null`},
		{"nested empties", `{"a":{},"b":[]}`, `{"a":{},"b":[]}`},
		{"unicode preserved", `{"a":"café"}`, `{"a":"café"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Canonicalize(json.RawMessage(tc.in))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("Canonicalize(%q) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

func TestCanonicalize_RejectsMalformed(t *testing.T) {
	for _, in := range []string{`{`, `{"a":}`, `{"a":1} trailing`, `nope`} {
		if _, err := Canonicalize(json.RawMessage(in)); err == nil {
			t.Errorf("Canonicalize(%q) should have failed", in)
		}
	}
}

func TestPayloadHash_StableAcrossSerialisation(t *testing.T) {
	h1, err := PayloadHash("cnmaestro", "device.set_radio_channel",
		json.RawMessage(`{"mac":"AA:BB","radio_id":2}`),
		json.RawMessage(`{"channel":"149","width":80}`))
	if err != nil {
		t.Fatal(err)
	}
	h2, err := PayloadHash("cnmaestro", "device.set_radio_channel",
		json.RawMessage(`{"radio_id":2,"mac":"AA:BB"}`),
		json.RawMessage(`{"width":80,"channel":"149"}`))
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("hash changed with key order: %s vs %s", h1, h2)
	}
}

// Every component of the payload must be covered by the hash. If any of these
// mutations produced an identical hash, an approved operation could be swapped
// for a different one without detection.
func TestPayloadHash_DetectsEveryFieldChange(t *testing.T) {
	const (
		plugin = "cnmaestro"
		action = "device.set_radio_channel"
	)
	target := json.RawMessage(`{"mac":"AA:BB","radio_id":2}`)
	params := json.RawMessage(`{"channel":"149"}`)

	baseline, err := PayloadHash(plugin, action, target, params)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name           string
		plugin, action string
		target, params json.RawMessage
	}{
		{"plugin swapped", "netbox", action, target, params},
		{"action swapped", plugin, "device.reboot", target, params},
		{"target changed", plugin, action, json.RawMessage(`{"mac":"CC:DD","radio_id":2}`), params},
		{"params changed", plugin, action, target, json.RawMessage(`{"channel":"36"}`)},
		{"param added", plugin, action, target, json.RawMessage(`{"channel":"149","width":20}`)},
		{"channel as number not string", plugin, action, target, json.RawMessage(`{"channel":149}`)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PayloadHash(tc.plugin, tc.action, tc.target, tc.params)
			if err != nil {
				t.Fatal(err)
			}
			if got == baseline {
				t.Fatalf("hash collision: %s produced the same hash as the baseline", tc.name)
			}
		})
	}
}

// Length prefixes exist so that field boundaries cannot be shifted. Without
// them, ("ab","c") and ("a","bc") would hash identically.
func TestPayloadHash_FieldBoundariesUnambiguous(t *testing.T) {
	empty := json.RawMessage(`{}`)
	h1, _ := PayloadHash("ab", "c", empty, empty)
	h2, _ := PayloadHash("a", "bc", empty, empty)
	if h1 == h2 {
		t.Fatal("field boundary ambiguity: plugin/action split is not covered by the hash")
	}
}

func TestCanonicalEqual(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"identical", `{"channel":"36"}`, `{"channel":"36"}`, true},
		{"reordered keys", `{"a":1,"b":2}`, `{"b":2,"a":1}`, true},
		{"drifted", `{"channel":"36"}`, `{"channel":"44"}`, false},
		{"both empty", ``, ``, true},
		{"malformed fails closed", `{`, `{`, false},
		{"one malformed fails closed", `{"a":1}`, `{`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CanonicalEqual(json.RawMessage(tc.a), json.RawMessage(tc.b))
			if got != tc.want {
				t.Fatalf("CanonicalEqual(%q,%q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestRecompute_MatchesStoredHashForUntouchedOperation(t *testing.T) {
	op := newOp(StateApproved)
	h, err := PayloadHash(op.Plugin, op.Action, op.Target, op.Params)
	if err != nil {
		t.Fatal(err)
	}
	op.PayloadHash = h

	got, err := Recompute(op)
	if err != nil {
		t.Fatal(err)
	}
	if got != op.PayloadHash {
		t.Fatal("recomputed hash must match for an unmodified operation")
	}

	// Tampering with the stored params must break the match, which is what the
	// claim guard relies on.
	op.Params = json.RawMessage(`{"v":999}`)
	got, err = Recompute(op)
	if err != nil {
		t.Fatal(err)
	}
	if got == op.PayloadHash {
		t.Fatal("tampered params must not reproduce the stored hash")
	}
}

// PreconditionsEqual(nil, nil) returned true, and the executor read that as a
// drift check that passed. It was not a check: two absent snapshots
// canonicalise to "null" and compare equal without anything being examined.
func TestCheckDrift(t *testing.T) {
	tests := []struct {
		name             string
		proposed, actual string
		want             DriftCheck
	}{
		{"neither declares anything", ``, ``, DriftNotChecked},
		{"both are JSON null", `null`, `null`, DriftNotChecked},
		{"both are empty objects", `{}`, `{}`, DriftNotChecked},
		{"same snapshot", `{"a":1}`, `{"a":1}`, DriftNone},
		{"key order does not matter", `{"a":1,"b":2}`, `{"b":2,"a":1}`, DriftNone},
		{"value changed", `{"a":1}`, `{"a":2}`, DriftDetected},
		{"the target grew state the proposal did not see", ``, `{"a":1}`, DriftDetected},
		{"the target lost the state the proposal depends on", `{"a":1}`, ``, DriftDetected},
		{"unreadable fails closed", `{`, `{`, DriftDetected},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckDrift(json.RawMessage(tc.proposed), json.RawMessage(tc.actual))
			if got != tc.want {
				t.Fatalf("CheckDrift(%q,%q) = %s, want %s", tc.proposed, tc.actual, got, tc.want)
			}
		})
	}
}

func TestDeclared(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{``, false}, {`null`, false}, {`{}`, false}, {`[]`, false},
		{`{"a":1}`, true}, {`[1]`, true}, {`0`, true}, {`false`, true},
		// Unreadable counts as declared so it reaches CanonicalEqual and
		// fails there, rather than being waved through as "asked for nothing".
		{`{`, true},
	} {
		if got := Declared(json.RawMessage(tc.raw)); got != tc.want {
			t.Errorf("Declared(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

// Assurance is the vocabulary split: a change mcpd planned, checked and
// confirmed is not the same evidence as a call a human waved through.
func TestOperationAssurance(t *testing.T) {
	tests := []struct {
		name          string
		verifiable    bool
		preconditions string
		want          Assurance
	}{
		{"all three proofs", true, `{"channel":"36"}`, AssuranceReviewedChange},
		{"cannot confirm the outcome", false, `{"channel":"36"}`, AssuranceGatedCall},
		{"cannot detect drift", true, ``, AssuranceGatedCall},
		{"neither", false, ``, AssuranceGatedCall},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			op := &Operation{
				Verifiable:    tc.verifiable,
				Preconditions: json.RawMessage(tc.preconditions),
			}
			if got := op.Assurance(); got != tc.want {
				t.Fatalf("Assurance() = %s, want %s", got, tc.want)
			}
		})
	}
}
