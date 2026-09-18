package threecx

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// Two ways a phone system's own build breaks a read, both found on live
// systems and both silent until somebody goes looking.
//
// A short-lived token turned every request into a fresh sign-in, and a queue
// property that arrived in a later build made every queue read on an older one
// fail outright. Neither looked like what it was: the first as latency and a
// rising anti-hacking count, the second as "queues are broken".

// A token whose whole life is shorter than the flat margin must still be worth
// holding. Before this, a sixty-second token was born expired -- the margin
// covered all of it -- so the client signed in again for every single request.
func TestToken_MarginNeverCoversTheWholeLife(t *testing.T) {
	cases := map[time.Duration]time.Duration{
		time.Hour:        tokenMargin,      // what every system used to answer
		10 * time.Minute: tokenMargin,      // comfortably longer than the margin
		2 * time.Minute:  tokenMargin,      // exactly twice, so the flat margin still fits
		time.Minute:      30 * time.Second, // the one that broke: half, not all
		30 * time.Second: 15 * time.Second,
		0:                tokenMargin, // no expiry given; the fallback life applies
	}
	for life, want := range cases {
		if got := marginFor(life); got != want {
			t.Errorf("a %s token should be given up %s before expiry, got %s", life, want, got)
		}
		if life > 0 && marginFor(life) >= life {
			t.Errorf("a %s token is given up after %s, so it is never usable at all", life, marginFor(life))
		}
	}
}

// The regression itself, through a client: a phone system that issues a
// sixty-second token is signed in to once for a burst of reads, not once per
// read.
//
// The cost of getting this wrong is not only latency. The password crosses the
// network on every request, and 3CX counts sign-ins -- successful ones too --
// against the anti-hacking limit that locks the source address out.
func TestToken_AShortTokenIsStillReused(t *testing.T) {
	var logins, reads int
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == loginPath {
			logins++
			w.Header().Set("Content-Type", "application/json")
			// Sixty seconds, which is what four of this deployment's phone
			// systems answer with.
			fmt.Fprintf(w, `{"Status":"AuthSuccess","Token":{"access_token":%q,"expires_in":60,"token_type":"Bearer"}}`, testToken)
			return
		}
		reads++
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(collection(0)))
	})

	p := pluginFor(t, srv.Client(), System{
		Name: "Acme", Host: srv.URL, Extension: "100", Password: "right-password",
	})
	ctx := context.Background()
	for range 5 {
		if _, err := p.listTrunks(ctx, trunksArgs{}); err != nil {
			t.Fatal(err)
		}
	}
	if reads < 5 {
		t.Fatalf("expected five reads, saw %d", reads)
	}
	if logins != 1 {
		t.Errorf("a burst of %d reads should cost one sign-in, cost %d -- the token "+
			"is being given up before it is ever used", reads, logins)
	}
}

// queuePBX is a phone system whose Queues type does not carry a set of
// properties, answering the way a 20.0.8 build answers a $select naming one.
//
// A set rather than one, because that is the shape of the real thing: a build
// without ComfortPrompts is also without ComfortPromptsEnabled and
// ComfortPromptsInterval, and 3CX names only the first it meets. Each pass
// therefore learns exactly one of them, which is what the retry has to survive.
type queuePBX struct {
	t *testing.T
	// without are the properties this build does not have.
	without []string
	// refusals counts how many reads it turned down, so a test can show the
	// bad projection is asked for once rather than on every call.
	refusals int
	reads    int
	selects  []string
}

func (q *queuePBX) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == loginPath {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"Status":"AuthSuccess","Token":{"access_token":%q,"expires_in":3600,"token_type":"Bearer"}}`, testToken)
		return
	}
	sel := r.URL.Query().Get("$select")
	q.selects = append(q.selects, sel)
	// Whole field names, not substrings: ComfortPrompts is a prefix of
	// ComfortPromptsEnabled, and a fixture that could not tell them apart
	// would refuse a projection the real system accepts.
	named := strings.Split(sel, ",")
	for _, missing := range q.without {
		if slices.Contains(named, missing) {
			q.refusals++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"error":{"code":"","message":"The query specified in the URI is not valid. Could not find a property named '%s' on type 'Pbx.Queue'.","details":[]}}`, missing)
			return
		}
	}
	q.reads++
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(collection(1, `{"Id":1,"Number":"800","Name":"Reception","PollingStrategy":"Hunt","OnHoldFile":"hold.wav"}`)))
}

func queuePlugin(t *testing.T, without ...string) (*Plugin, *queuePBX) {
	t.Helper()
	fake := &queuePBX{t: t, without: without}
	srv := newServer(t, fake.ServeHTTP)
	p := pluginFor(t, srv.Client(), System{
		Name: "Acme", Host: srv.URL, Extension: "100", Password: "right-password",
	})
	return p, fake
}

// A queue property this build does not have costs that property, not every
// queue.
//
// 3CX refuses the whole query when one named property is unknown, so
// ComfortPrompts -- added in a later build -- made list_queues, get_queue and
// search_audio_usage fail outright on a 20.0.8 system while working on 20.0.9.
// The answer is the rest of the queue, and a note saying what is missing.
func TestQueues_APropertyThisBuildLacksIsDroppedNotFatal(t *testing.T) {
	// All three, as a build without the feature has it.
	p, fake := queuePlugin(t, "ComfortPrompts", "ComfortPromptsEnabled", "ComfortPromptsInterval")
	ctx := context.Background()

	out, err := p.listQueues(ctx, queuesArgs{})
	if err != nil {
		t.Fatalf("a build without ComfortPrompts should still list its queues: %v", err)
	}
	if len(out.Queues) != 1 || out.Queues[0].Number != "800" {
		t.Fatalf("queues: %+v", out.Queues)
	}
	// The answer says what it could not ask about. A queue reported with no
	// comfort prompt because the build cannot say is not a queue with no
	// comfort prompt.
	for _, want := range []string{"ComfortPrompts", "ComfortPromptsEnabled", "ComfortPromptsInterval"} {
		if !slices.Contains(out.Unavailable, want) {
			t.Errorf("the listing should name %q as dropped, got %v", want, out.Unavailable)
		}
	}
	// One refused request per property the build lacks, and no more: the
	// projection that works is reached by learning, not by retrying blindly.
	if fake.refusals != 3 {
		t.Errorf("three absent properties should cost three refusals, cost %d", fake.refusals)
	}

	// The second call goes straight to the projection that works: the build's
	// answer is remembered per phone system, so the refused request is paid
	// once rather than on every call.
	if _, err := p.listQueues(ctx, queuesArgs{}); err != nil {
		t.Fatal(err)
	}
	if fake.refusals != 3 {
		t.Errorf("a second listing repeated a refused projection: %d refusals", fake.refusals)
	}

	// And the detailed read, which is the one a caller reaches for next.
	q, err := p.getQueue(ctx, queueArgs{Queue: "800"})
	if err != nil {
		t.Fatalf("get_queue should answer too: %v", err)
	}
	if q.Number != "800" {
		t.Errorf("queue: %+v", q)
	}
	var said bool
	for _, u := range q.Unavailable {
		if strings.Contains(u, "ComfortPrompts") {
			said = true
		}
	}
	if !said {
		t.Errorf("get_queue should say the property is not reported, got %v", q.Unavailable)
	}
	if fake.refusals != 3 {
		t.Errorf("get_queue repeated a refused projection: %d refusals", fake.refusals)
	}

	// Every projection after the first drops the property and keeps the rest:
	// dropping fields until an error goes away would answer a different
	// question from the one asked.
	last := fake.selects[len(fake.selects)-1]
	for _, want := range []string{"Id", "Number", "Name", "OnHoldFile", "AnnouncementInterval"} {
		if !strings.Contains(last, want) {
			t.Errorf("the working projection lost %q: %s", want, last)
		}
	}
	if strings.Contains(last, "ComfortPrompts") {
		t.Errorf("the working projection still names the absent property: %s", last)
	}
}

// A 400 that is not about a missing property is still a failure, not something
// to retry with a narrower projection.
func TestQueues_AnOrdinaryRefusalIsNotRetried(t *testing.T) {
	var attempts int
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == loginPath {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"Status":"AuthSuccess","Token":{"access_token":%q,"expires_in":3600,"token_type":"Bearer"}}`, testToken)
			return
		}
		attempts++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"","message":"The query specified in the URI is not valid. Syntax error at position 0.","details":[]}}`))
	})
	p := pluginFor(t, srv.Client(), System{
		Name: "Acme", Host: srv.URL, Extension: "100", Password: "right-password",
	})
	_, err := p.listQueues(context.Background(), queuesArgs{})
	if err == nil {
		t.Fatal("a syntax error is a failure, not a projection to narrow")
	}
	if attempts != 1 {
		t.Errorf("it should be tried once, was tried %d times", attempts)
	}
	if !strings.Contains(err.Error(), "Syntax error") {
		t.Errorf("the refusal should carry what the phone system said, got %v", err)
	}
}

// The dropped property never reaches a caller as though it were data, and no
// credential rides along with the note.
func TestQueues_TheNoteCarriesNothingItShouldNot(t *testing.T) {
	p, _ := queuePlugin(t, "ComfortPrompts")
	out, err := p.listQueues(context.Background(), queuesArgs{})
	if err != nil {
		t.Fatal(err)
	}
	mustNotContain(t, out, "right-password", "Bearer", testToken)
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"unavailable":["ComfortPrompts"]`) {
		t.Errorf("the encoded answer should carry the note: %s", raw)
	}
}

// A gateway status is the phone system cutting its own request off, and says
// so rather than blaming the address.
//
// 3CX answers it with an HTML error page, which the generic summary reads as
// "the address may be reaching a web server rather than the phone system" --
// exactly wrong when the address is right and the query is merely too wide.
// The message a model acts on has to say which.
func TestUpstream_AGatewayTimeoutSaysToAskForLess(t *testing.T) {
	html := []byte("<html><head><title>504 Gateway Time-out</title></head><body>…</body></html>")
	for _, status := range []int{502, 503, 504} {
		err := explainRequestFailure(status, "CallHistoryView", html)
		text := err.Error()
		for _, want := range []string{"took too long", "The address is fine", "narrower time window"} {
			if !strings.Contains(text, want) {
				t.Errorf("HTTP %d should say %q, got %v", status, want, text)
			}
		}
		if strings.Contains(text, "may be reaching a web server") {
			t.Errorf("HTTP %d should not blame the address: %v", status, text)
		}
	}
	// A 500 is still a failure of the thing itself, and still reads as one.
	if text := explainRequestFailure(500, "Users", []byte("boom")).Error(); !strings.Contains(text, "failed answering") {
		t.Errorf("a 500 should still report the failure plainly, got %v", text)
	}
}
