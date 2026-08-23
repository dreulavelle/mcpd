package registry

// The merge, with all four catalogues in play. The helpers -- stubSource, at,
// names, statusOf -- are multi_test.go's; these are the behaviours that only
// appear once there is more than a pair.

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// TestMulti_FourSourcesResolveOneEntryByPreference.
//
// Four catalogues, one server, four names for it. Deduplication is by address
// -- a name cannot do the job, since no rule turns "app.linear/linear" into
// "linear" -- and the copy kept is the most preferred source's.
//
// The order encodes one idea applied four times: how far the entry is from the
// party that operates the server. The official registry is where a publisher
// registers their own; PulseMCP passes that same document through; Docker's is
// a document this host composed from a third party's description; and Smithery
// describes its own proxy in front of the server rather than the server. So
// the official registry's copy survives, and the response still accounts for
// all four.
func TestMulti_FourSourcesResolveOneEntryByPreference(t *testing.T) {
	const endpoint = "https://mcp.linear.app/mcp"
	sources := []Client{
		&stubSource{name: officialSource, pages: map[string]Page{
			"": {Entries: []Entry{at(officialSource, "app.linear/linear", endpoint)}},
		}},
		&stubSource{name: pulseMCPSource, pages: map[string]Page{
			// Mirrored from the official registry, so the same document at the
			// same address -- which is the collision that actually happens in
			// practice, and the one this order is for.
			"": {Entries: []Entry{at(pulseMCPSource, "app.linear/linear", endpoint)}},
		}},
		&stubSource{name: dockerSource, pages: map[string]Page{
			// The address written a little differently: uppercase host and a
			// trailing slash are noise.
			"": {Entries: []Entry{at(dockerSource, "linear", "https://MCP.Linear.app/mcp/")}},
		}},
		&stubSource{name: smitherySource, pages: map[string]Page{
			"": {Entries: []Entry{at(smitherySource, "linear", endpoint)}},
		}},
	}

	page, err := NewMulti(sources...).List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 {
		t.Fatalf("entries = %v, want one -- four names for one address is one server", names(page))
	}
	if got := page.Entries[0]; got.Source != officialSource {
		t.Errorf("kept %s's copy, want the official registry's", got.Source)
	}

	// Every source still accounts for itself, and only the winner contributed.
	for _, name := range []string{officialSource, pulseMCPSource, dockerSource, smitherySource} {
		s, ok := statusOf(page, name)
		if !ok {
			t.Fatalf("%s is missing from the page's own account of itself", name)
		}
		if !s.OK {
			t.Errorf("%s reports not OK", name)
		}
		want := 0
		if name == officialSource {
			want = 1
		}
		if s.Entries != want {
			t.Errorf("%s contributed %d, want %d", name, s.Entries, want)
		}
	}
}

// TestMulti_ASmitheryGatewayEntryIsNotThePublishersOwnServer.
//
// The other half of the rule, and the one that is easy to get wrong. Smithery
// addresses every server it hosts at its own gateway, so a Smithery row and an
// official row for what is recognisably the same project have different
// addresses and do not merge. That is right rather than a miss: dialling the
// publisher's endpoint with the publisher's key and dialling Smithery's
// gateway with a Smithery key are two different servers by every test that
// matters here -- different address, different credential, different party to
// trust. Merging them on the strength of a similar name would hide one of two
// real choices.
func TestMulti_ASmitheryGatewayEntryIsNotThePublishersOwnServer(t *testing.T) {
	official := &stubSource{name: officialSource, pages: map[string]Page{
		"": {Entries: []Entry{at(officialSource, "com.exa/exa-mcp-server", "https://mcp.exa.ai/mcp")}},
	}}
	smithery := &stubSource{name: smitherySource, pages: map[string]Page{
		"": {Entries: []Entry{at(smitherySource, "exa", "https://server.smithery.ai/exa/mcp")}},
	}}

	page, err := NewMulti(official, smithery).List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 2 {
		t.Errorf("entries = %v, want both -- two addresses are two servers", names(page))
	}
}

// TestMulti_AnyOneOfFourDownStillAnswers, and the response says which.
//
// One source's failure is one source's, whichever one it is. Table-driven so
// that no single catalogue is the one that happens to get tested.
func TestMulti_AnyOneOfFourDownStillAnswers(t *testing.T) {
	all := []string{officialSource, pulseMCPSource, dockerSource, smitherySource}

	for _, broken := range all {
		t.Run(broken, func(t *testing.T) {
			sources := make([]Client, 0, len(all))
			for _, name := range all {
				if name == broken {
					sources = append(sources, &stubSource{
						name: name,
						err:  fmt.Errorf("registry: %s could not be reached: dial tcp: refused", name),
					})
					continue
				}
				sources = append(sources, &stubSource{name: name, pages: map[string]Page{
					"": {Entries: []Entry{
						at(name, name+"/one", "https://"+name+".example/mcp"),
					}},
				}})
			}

			page, err := NewMulti(sources...).List(context.Background(), Query{})
			if err != nil {
				t.Fatalf("one source being down must not fail the page: %v", err)
			}
			if len(page.Entries) != len(all)-1 {
				t.Errorf("entries = %v, want the three that answered", names(page))
			}

			bad, ok := statusOf(page, broken)
			if !ok {
				t.Fatal("the failing source is not reported; the page silently lost a catalogue")
			}
			if bad.OK {
				t.Error("the failing source reports OK")
			}
			if !strings.Contains(bad.Error, "could not be reached") {
				t.Errorf("error = %q, want the reason an operator would act on", bad.Error)
			}
			for _, name := range all {
				if name == broken {
					continue
				}
				if s, ok := statusOf(page, name); !ok || !s.OK || s.Entries != 1 {
					t.Errorf("%s status = %+v, want it to have answered with one entry", name, s)
				}
			}
		})
	}
}

// TestMulti_ASourcesNoteSurvivesTheMerge.
//
// A note is a source's own account of the answer it gave -- Smithery's listing
// stopping at five hundred of ten thousand is the case it exists for. The
// merge rebuilds each SourceStatus, because how many entries survived
// deduplication is not something a source can know, and rebuilding it must not
// silently drop the one field the source did have something to say in.
func TestMulti_ASourcesNoteSurvivesTheMerge(t *testing.T) {
	const note = "this listing is bounded; search reaches the rest"
	smithery := &stubSource{name: smitherySource, pages: map[string]Page{
		"": {
			Entries: []Entry{at(smitherySource, "brave", "https://server.smithery.ai/brave/mcp")},
			Sources: []SourceStatus{{Source: smitherySource, OK: true, Note: note}},
		},
	}}
	official := &stubSource{name: officialSource, pages: map[string]Page{
		"": {Entries: []Entry{at(officialSource, "io.example/one", "https://one.example/mcp")}},
	}}

	page, err := NewMulti(official, smithery).List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := statusOf(page, smitherySource)
	if !ok {
		t.Fatal("smithery is missing from the page")
	}
	if got.Note != note {
		t.Errorf("note = %q, want %q carried across the merge", got.Note, note)
	}
	// And a source with nothing to say does not acquire somebody else's note.
	if s, _ := statusOf(page, officialSource); s.Note != "" {
		t.Errorf("official note = %q, want none", s.Note)
	}
}
