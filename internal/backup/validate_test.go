package backup

import (
	"errors"
	"strings"
	"testing"
	"unicode"
)

// Every refusal an operator can act on is a sentence.
//
// InvalidDestination.Sentence goes straight into the dashboard's answer, with
// no prefix stripped and nothing prepended, so a fragment like "give the
// bucket" arrives beside a heading as a half-line of somebody else's code.
// docs/dashboard-copy.md is the rule: sentence case, and a full stop, because
// what a person reads is a sentence whatever produced it.
//
// The table is every branch of Validate that refuses, which is what makes this
// a check on all of them rather than on the ones somebody remembered.
func TestEveryDestinationRefusalIsASentence(t *testing.T) {
	// A directory that exists and is not the data directory, so the local
	// cases fail on what they are meant to fail on.
	dir := t.TempDir()

	cases := []struct {
		name string
		d    Destination
	}{
		{"no name", Destination{Kind: KindLocal, Policy: DefaultPolicy}},
		{"a kind this build has not", Destination{
			Name: "n", Kind: "ftp", Policy: DefaultPolicy}},
		{"keeping nothing", Destination{
			Name: "n", Kind: KindLocal, Settings: Settings{Path: dir}}},
		{"a negative count", Destination{
			Name: "n", Kind: KindLocal, Settings: Settings{Path: dir},
			Policy: Policy{KeepLast: 1, KeepDaily: -1}}},

		{"no directory given", Destination{
			Name: "n", Kind: KindLocal, Policy: DefaultPolicy}},
		{"a directory that is not there", Destination{
			Name: "n", Kind: KindLocal, Policy: DefaultPolicy,
			Settings: Settings{Path: dir + "/nope"}}},

		{"no host", Destination{
			Name: "n", Kind: KindSFTP, Policy: DefaultPolicy}},
		{"no user", Destination{
			Name: "n", Kind: KindSFTP, Policy: DefaultPolicy,
			Settings: Settings{Host: "nas"}}},
		{"a port that is not one", Destination{
			Name: "n", Kind: KindSFTP, Policy: DefaultPolicy,
			Settings: Settings{Host: "nas", Username: "u", Port: 99999}}},
		{"a host key that is not a fingerprint", Destination{
			Name: "n", Kind: KindSFTP, Policy: DefaultPolicy, HostKey: "ssh-rsa AAAA",
			Settings: Settings{Host: "nas", Username: "u"}}},

		{"no bucket", Destination{
			Name: "n", Kind: KindS3, Policy: DefaultPolicy}},
		{"no endpoint", Destination{
			Name: "n", Kind: KindS3, Policy: DefaultPolicy,
			Settings: Settings{Bucket: "b"}}},
		{"an endpoint with a scheme", Destination{
			Name: "n", Kind: KindS3, Policy: DefaultPolicy,
			Settings: Settings{Bucket: "b", Endpoint: "https://s3.example.com"}}},
		{"plain HTTP to somewhere else", Destination{
			Name: "n", Kind: KindS3, Policy: DefaultPolicy,
			Settings: Settings{Bucket: "b", Endpoint: "s3.example.com", AllowInsecure: true}}},

		{"no address", Destination{
			Name: "n", Kind: KindWebDAV, Policy: DefaultPolicy}},
		{"a credential in the address", Destination{
			Name: "n", Kind: KindWebDAV, Policy: DefaultPolicy,
			Settings: Settings{URL: "https://u:p@nas.example.com/backups"}}},
		{"plain HTTP without saying so", Destination{
			Name: "n", Kind: KindWebDAV, Policy: DefaultPolicy,
			Settings: Settings{URL: "http://nas.example.com/backups"}}},
		{"a scheme that is neither", Destination{
			Name: "n", Kind: KindWebDAV, Policy: DefaultPolicy,
			Settings: Settings{URL: "ftp://nas.example.com/backups"}}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := c.d
			err := d.Validate(dir)
			if err == nil {
				t.Fatalf("this should have been refused")
			}
			var bad *InvalidDestination
			if !errors.As(err, &bad) {
				t.Fatalf("refused with %T, which the handler cannot classify: %v", err, err)
			}
			s := bad.Sentence
			if s == "" {
				t.Fatal("refused with no sentence at all")
			}
			// A leading %q or %s is a value being named, and starts the
			// sentence as legitimately as a capital does. So does "mcpd",
			// which is the product's name and is lowercase everywhere --
			// capitalising it at the start of a sentence would be the one
			// place in the dashboard that spelled it differently.
			if first := []rune(s)[0]; unicode.IsLower(first) && !strings.HasPrefix(s, "mcpd ") {
				t.Errorf("starts mid-sentence: %q", s)
			}
			if !strings.HasSuffix(s, ".") {
				t.Errorf("does not end as a sentence: %q", s)
			}
		})
	}
}
