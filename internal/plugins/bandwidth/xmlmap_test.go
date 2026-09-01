package bandwidth

import (
	"net/url"
	"strings"
	"testing"
)

// The trap this whole file exists for: XML cannot say "this is a list", so a
// collection with one member decodes to a map and the same collection with two
// decodes to a slice. Code written against the two-member case then breaks on
// the account that happens to have one number -- silently, and in production,
// because the test fixture had two.
func TestListOfIsASliceWhateverTheDocumentSaid(t *testing.T) {
	for name, body := range map[string]string{
		"two members": `<TNsResponse><TelephoneNumbers>
			<TelephoneNumber>9195551234</TelephoneNumber>
			<TelephoneNumber>9195555678</TelephoneNumber>
		</TelephoneNumbers></TNsResponse>`,
		"one member": `<TNsResponse><TelephoneNumbers>
			<TelephoneNumber>9195551234</TelephoneNumber>
		</TelephoneNumbers></TNsResponse>`,
	} {
		t.Run(name, func(t *testing.T) {
			rec, err := decodeXML([]byte(body))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			got := listOf(rec, "TelephoneNumbers", "TelephoneNumber")
			if len(got) == 0 {
				t.Fatal("no records came back")
			}
			if got[0]["TelephoneNumber"] != "9195551234" {
				t.Errorf("first record = %v", got[0])
			}
		})
	}
}

// An empty collection is an account with no numbers, not a failure. Bandwidth
// omits the container entirely rather than sending an empty one.
func TestListOfIsEmptyRatherThanAnErrorWhenNothingIsThere(t *testing.T) {
	rec, err := decodeXML([]byte(`<TNsResponse><TotalCount>0</TotalCount></TNsResponse>`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := listOf(rec, "TelephoneNumbers", "TelephoneNumber"); len(got) != 0 {
		t.Fatalf("want no records, got %v", got)
	}
}

// The outer <XxxResponse> names the request rather than the data. Keeping it
// would put a pointless level of nesting in front of every answer.
func TestTheResponseEnvelopeIsUnwrapped(t *testing.T) {
	rec, err := decodeXML([]byte(
		`<SiteResponse><Site><Id>407</Id><Name>Tulsa</Name></Site></SiteResponse>`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, unwanted := rec["SiteResponse"]; unwanted {
		t.Fatal("the envelope was not unwrapped")
	}
	site, ok := rec["Site"].(Record)
	if !ok {
		t.Fatalf("Site is not a record: %#v", rec["Site"])
	}
	if site["Name"] != "Tulsa" {
		t.Errorf("Name = %v", site["Name"])
	}
}

func TestNestedElementsAndAttributes(t *testing.T) {
	rec, err := decodeXML([]byte(`<R>
		<Order id="o-1"><Status>COMPLETE</Status><Count>2</Count></Order>
	</R>`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	order, ok := rec["Order"].(Record)
	if !ok {
		t.Fatalf("Order is not a record: %#v", rec["Order"])
	}
	if order["Status"] != "COMPLETE" {
		t.Errorf("Status = %v", order["Status"])
	}
	// Attributes are kept under an @ prefix rather than dropped: silently
	// discarding data because it usually is not there is how a field goes
	// missing for a year.
	if order["@id"] != "o-1" {
		t.Errorf("the id attribute was lost: %#v", order)
	}
}

func TestMalformedXMLIsAnErrorRatherThanAnEmptyAnswer(t *testing.T) {
	for name, body := range map[string]string{
		"not closed": `<R><Order>`,
		"not xml":    `{"this":"is json"}`,
		"empty":      ``,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeXML([]byte(body)); err == nil {
				t.Fatal("a body that is not a response decoded without error")
			}
		})
	}
}

// The Dashboard answers some refusals with 200 and an error in the body. Left
// alone that reads as an empty result, which is the worst outcome: a model
// told nothing is there will say so with confidence.
func TestAnErrorInsideA200IsAnError(t *testing.T) {
	for name, body := range map[string]string{
		"flat":   `<R><ErrorCode>5005</ErrorCode><Description>Account not found</Description></R>`,
		"nested": `<R><Error><Code>5005</Code><Description>Account not found</Description></Error></R>`,
	} {
		t.Run(name, func(t *testing.T) {
			rec, err := decodeXML([]byte(body))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			err = dashboardError(rec, "/accounts/1/tns")
			if err == nil {
				t.Fatal("an error carried inside a 200 was read as a result")
			}
			if !strings.Contains(err.Error(), "Account not found") {
				t.Errorf("the message drops the description: %v", err)
			}
		})
	}
}

func TestASuccessfulResponseIsNotMistakenForAnError(t *testing.T) {
	rec, err := decodeXML([]byte(`<R><TotalCount>3</TotalCount></R>`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := dashboardError(rec, "/accounts/1/tns"); err != nil {
		t.Fatalf("a good response was reported as an error: %v", err)
	}
}

// The bug this exists for: /disconnects returned 108 kB, /tnoptions 175 kB and
// /applications 31 kB, and all three were reported as zero rows because the
// element name guessed here did not match the one Bandwidth used. An operator
// told their estate is empty believes it.
func TestCollectFindsAListWhateverTheWrapperIsCalled(t *testing.T) {
	for name, body := range map[string]string{
		"a wrapper nobody guessed": `<Response><SomethingNobodyGuessed>
			<Item><Id>1</Id></Item><Item><Id>2</Id></Item>
		</SomethingNobodyGuessed></Response>`,
		"directly under the root": `<Response>
			<Item><Id>1</Id></Item><Item><Id>2</Id></Item>
		</Response>`,
		"nested two deep": `<Response><Outer><Inner>
			<Item><Id>1</Id></Item><Item><Id>2</Id></Item>
		</Inner></Outer></Response>`,
	} {
		t.Run(name, func(t *testing.T) {
			rec, err := decodeXML([]byte(body))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			// Deliberately the wrong names, which is the situation in
			// production: the guess is a preference, not a requirement.
			got, note := collect(rec, "WrongContainer", "WrongElement")
			if len(got) != 2 {
				t.Fatalf("want 2 records, got %d (note %q)", len(got), note)
			}
			if note != "" {
				t.Errorf("a found list should carry no caveat: %q", note)
			}
		})
	}
}

// A genuinely empty collection must stay empty and silent, or every empty
// answer grows a warning and the warning stops meaning anything.
func TestCollectStaysQuietOnAGenuinelyEmptyResponse(t *testing.T) {
	rec, err := decodeXML([]byte(`<Response><TotalCount>0</TotalCount><Links></Links></Response>`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	got, note := collect(rec, "Things", "Thing")
	if len(got) != 0 {
		t.Fatalf("want no records, got %v", got)
	}
	if note != "" {
		t.Errorf("an empty collection should need no caveat: %q", note)
	}
}

// And when there is content that cannot be read as a list, the count must not
// be presented as a count. "Unknown" and "none" are different answers.
func TestCollectRefusesToCallUnreadableContentEmpty(t *testing.T) {
	rec, err := decodeXML([]byte(
		`<Response><TotalCount>3</TotalCount><Payload>something structured</Payload></Response>`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	got, note := collect(rec, "Things", "Thing")
	if len(got) != 0 {
		t.Fatalf("got %v", got)
	}
	if note == "" {
		t.Fatal("content that was not recognised was reported as an empty collection")
	}
	if !strings.Contains(note, "unknown rather than none") {
		t.Errorf("the caveat does not distinguish unknown from none: %q", note)
	}
}

// The Dashboard refuses a size without a page -- in as many words on /orders,
// and as a bare 404 elsewhere, which reads as an empty collection and is not.
func TestSetPageAlwaysSendsBoth(t *testing.T) {
	q := url.Values{}
	setPage(q, 0, 200)
	if q.Get("page") != "1" || q.Get("size") != "200" {
		t.Fatalf("page/size = %q/%q, want 1/200", q.Get("page"), q.Get("size"))
	}
	setPage(q, 3, 50)
	if q.Get("page") != "3" || q.Get("size") != "50" {
		t.Fatalf("page/size = %q/%q, want 3/50", q.Get("page"), q.Get("size"))
	}
}

// A collection holding exactly one row must not read as an empty collection.
//
// discoverList looks for a repeated element, which cannot see a list of one.
// Measured against a live account: a port-out listing returned nothing at
// limit=1 and two rows at limit=2, because the element name was wrong and only
// the plural case could be rescued. A listing narrowed to a single match is one
// of the most common things anybody asks for.
func TestCollect_FindsACollectionOfOne(t *testing.T) {
	// The shape the Dashboard actually sends, with the element named something
	// the caller guessed wrong.
	one := Record{
		"TotalCount": "1",
		"Links":      "",
		"lnpPortInfoForGivenStatus": Record{
			"OrderId":          "e271c0ed",
			"ProcessingStatus": "COMPLETE",
		},
	}
	items, note := collect(one, "", "SomeNameNobodyGuessed")
	if len(items) != 1 {
		t.Fatalf("a collection of one should yield one row, got %d (note %q)", len(items), note)
	}
	if items[0]["OrderId"] != "e271c0ed" {
		t.Errorf("the wrong record was returned: %v", items[0])
	}

	// Two candidates and no repetition is ambiguous, and a wrong row is worse
	// than saying the shape was not recognised.
	ambiguous := Record{
		"TotalCount": "2",
		"Alpha":      Record{"OrderId": "a"},
		"Beta":       Record{"OrderId": "b"},
	}
	items, note = collect(ambiguous, "", "SomeNameNobodyGuessed")
	if len(items) != 0 {
		t.Errorf("two candidates cannot be disambiguated; got %d rows", len(items))
	}
	if note == "" {
		t.Error("an unrecognised shape must say so rather than read as empty")
	}

	// And the named element still wins when it is right.
	items, _ = collect(one, "", "lnpPortInfoForGivenStatus")
	if len(items) != 1 {
		t.Errorf("the named lookup should still work, got %d", len(items))
	}
}
