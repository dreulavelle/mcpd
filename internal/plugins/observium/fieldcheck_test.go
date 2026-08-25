package observium

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// envelopeMeta are the envelope's own fields. Whatever else a response carries
// is the collection, whatever it happens to be called.
var envelopeMeta = map[string]bool{
	"status": true, "count": true, "pagesize": true, "pageno": true,
	"countpage": true, "message": true, "entity_cache": true,
}

// TestIntegration_ReadSurface checks two things this package currently takes
// on trust, against a real installation.
//
// First, the envelope key. A collection is answered under a name Observium
// chooses per endpoint, and decodeCollection returns "no results" rather than
// an error when the configured name is not there -- which means a wrong key is
// indistinguishable, to a caller, from an estate with nothing in it. The
// published contract disagrees with apiPaths on several endpoints, and a
// disagreement that costs nothing to check should be checked rather than
// argued about.
//
// Second, the field sets. Most were written from naming conventions rather
// than from an answer anybody had seen. A name allow-listed but never returned
// is a field somebody thinks they are keeping and is not; a name returned but
// not allow-listed is a field a listing silently stopped carrying.
//
// It prints field *names* and never values, so the output is safe to paste. It
// asks for five rows per endpoint, one page each, and reads only -- every
// request goes through the same read-only transport and deny-list the tools
// do. It is safe against production monitoring, which is what it is for.
func TestIntegration_ReadSurface(t *testing.T) {
	if os.Getenv("OBSERVIUM_TEST_URL") == "" {
		t.Skip("set OBSERVIUM_TEST_URL and OBSERVIUM_TEST_TOKEN to check the read surface against a real Observium")
	}
	p := integrationPlugin(t)

	entities := make([]Entity, 0, len(apiPaths))
	for e := range apiPaths {
		entities = append(entities, e)
	}
	sort.Slice(entities, func(i, j int) bool { return entities[i] < entities[j] })

	var wrongKey, unreadable []string
	for _, entity := range entities {
		route := apiPaths[entity]

		params := url.Values{}
		params.Set("pagesize", "5")
		params.Set("pageno", "1")

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, raw, err := p.client.do(ctx, route.path, params)
		cancel()
		if err != nil {
			unreadable = append(unreadable, string(entity))
			t.Logf("\n=== %s (%s) ===\n  NOT READ: %v", entity, route.path, err)
			continue
		}

		var body map[string]json.RawMessage
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Logf("\n=== %s (%s) ===\n  response was not a JSON object: %v", entity, route.path, err)
			continue
		}

		var candidates []string
		for name := range body {
			if !envelopeMeta[name] {
				candidates = append(candidates, name)
			}
		}
		sort.Strings(candidates)

		t.Logf("\n=== %s (%s) ===", entity, route.path)
		t.Logf("  configured key: %q | keys actually present: %v", route.key, candidates)

		if _, present := body[route.key]; !present {
			wrongKey = append(wrongKey, string(entity))
			t.Errorf("  WRONG KEY: %s reads %q, which this installation does not answer under. "+
				"Every call to it returns an empty result and reports no error.", entity, route.key)
		}

		// Field names come from whichever key actually holds entities, so the
		// report is useful even where the configured key is wrong.
		fields := map[string]bool{}
		var rows int
		for _, name := range candidates {
			items, err := decodeCollection(raw, name)
			if err != nil || len(items) == 0 {
				continue
			}
			for _, item := range items {
				rows++
				for f := range item {
					fields[f] = true
				}
			}
		}
		if rows == 0 {
			t.Logf("  nothing to sample (none monitored, or not readable by this token)")
			continue
		}

		actual := make([]string, 0, len(fields))
		for f := range fields {
			actual = append(actual, f)
		}
		sort.Strings(actual)

		declared, ok := summaryFields[entity]
		if !ok {
			t.Logf("  %d fields returned, no field set declared yet:\n    %s",
				len(actual), strings.Join(actual, ", "))
			continue
		}
		want := map[string]bool{}
		for _, name := range declared {
			want[name] = true
		}

		var phantom, dropped []string
		for _, name := range declared {
			if !fields[name] {
				phantom = append(phantom, name)
			}
		}
		for _, name := range actual {
			if !want[name] {
				dropped = append(dropped, name)
			}
		}

		t.Logf("  %d fields returned, %d kept, %d dropped", len(actual), len(actual)-len(dropped), len(dropped))
		if len(phantom) > 0 {
			t.Logf("  ALLOW-LISTED BUT NEVER RETURNED (dead names):\n    %s", strings.Join(phantom, ", "))
		}
		if len(dropped) > 0 {
			t.Logf("  DROPPED (check for anything worth keeping):\n    %s", strings.Join(dropped, ", "))
		}
		if len(actual) == len(dropped) {
			t.Errorf("  %s: the field set matches nothing this installation returns", entity)
		}
	}

	if len(wrongKey) > 0 {
		t.Logf("\nEndpoints whose configured key is wrong, and so silently return nothing: %s",
			strings.Join(wrongKey, ", "))
	}
	if len(unreadable) > 0 {
		t.Logf("\nEndpoints this token could not read: %s", strings.Join(unreadable, ", "))
	}
}
