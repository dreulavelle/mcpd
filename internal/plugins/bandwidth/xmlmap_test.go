package bandwidth

import (
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
