package bandwidth

import (
	"context"
	"strings"
	"testing"
)

// The number on a failed message is the whole explanation of why it did not
// arrive, and nothing in the API turns it into words.
func TestGetErrorReasonExplainsAKnownCode(t *testing.T) {
	p := &Plugin{}
	for code, want := range map[string]struct {
		meaning   string
		transient bool
	}{
		"4720": {"invalid-destination-address", false},
		"4773": {"inactive-campaign", false},
		"5100": {"temporary-app-error", true},
		"5600": {"destination-carrier-queue-full", true},
	} {
		got, err := p.getErrorReason(context.Background(), ErrorReasonInput{Code: code})
		if err != nil {
			t.Fatalf("%s: %v", code, err)
		}
		if got.Meaning != want.meaning {
			t.Errorf("%s meaning = %q, want %q", code, got.Meaning, want.meaning)
		}
		// Whether to send it again is the second question every time, and the
		// answer differs by class rather than by code.
		if got.Transient != want.transient {
			t.Errorf("%s transient = %v, want %v", code, got.Transient, want.transient)
		}
		if got.Category == "" || got.Advice == "" {
			t.Errorf("%s came back without a category or advice: %+v", code, got)
		}
	}
}

// A code arriving as text is not a mistake worth making somebody debug: it is
// a field relayed from somewhere else.
func TestGetErrorReasonAcceptsTheCodeAsWritten(t *testing.T) {
	p := &Plugin{}
	for _, in := range []string{"4720", " 4720 "} {
		got, err := p.getErrorReason(context.Background(), ErrorReasonInput{Code: in})
		if err != nil || got.Code != 4720 {
			t.Errorf("%q: got %+v, err %v", in, got, err)
		}
	}
}

// Unknown to this table is not unknown to Bandwidth. Saying so is the
// difference between an operator checking the page and one concluding the code
// was invented -- and the range still carries the useful half of the answer.
func TestAnUnknownCodeSaysSoAndStillHelps(t *testing.T) {
	p := &Plugin{}
	got, err := p.getErrorReason(context.Background(), ErrorReasonInput{Code: "4999"})
	if err != nil {
		t.Fatalf("an unknown code was an error: %v", err)
	}
	if got.Meaning != "" {
		t.Errorf("an unknown code was given a meaning: %q", got.Meaning)
	}
	if !strings.Contains(got.Note, "not in this table") {
		t.Errorf("the note does not say the table is the limit: %q", got.Note)
	}
	if !strings.Contains(got.Note, "dev.bandwidth.com") {
		t.Errorf("the note does not point anywhere: %q", got.Note)
	}
	if !strings.Contains(got.Note, "carrier refusing") {
		t.Errorf("the range hint is missing: %q", got.Note)
	}
}

func TestNonNumericCodesAreRefused(t *testing.T) {
	p := &Plugin{}
	for _, in := range []string{"", "delivered", "4720-ish"} {
		if _, err := p.getErrorReason(context.Background(), ErrorReasonInput{Code: in}); err == nil {
			t.Errorf("%q was accepted as an error code", in)
		}
	}
}

// Every code must resolve to a class that exists, or the answer carries an
// empty category and reads as though nothing is known about it.
func TestEveryCodeHasAClass(t *testing.T) {
	for _, code := range knownErrorCodes() {
		entry := messagingErrors[code]
		if _, ok := errorClasses[entry.Class]; !ok {
			t.Errorf("%d has class %q, which is not defined", code, entry.Class)
		}
	}
	if n := len(knownErrorCodes()); n < 80 {
		t.Errorf("the table holds %d codes; the published set is around 80", n)
	}
}
