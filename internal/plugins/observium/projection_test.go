package observium

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// The bug this exists for: a level 6 token gets snmp_community on every
// device, and every field Observium returned went into the tool result. A
// community string in a tool result is a live SNMP credential in a model's
// context.
func TestNarrow_CredentialsAreNeverReturned(t *testing.T) {
	for _, v := range []view{viewSummary, viewFull} {
		t.Run(string(v), func(t *testing.T) {
			items := []map[string]any{{
				"device_id":       "1",
				"hostname":        "core-1",
				"snmp_community":  "public",
				"snmp_authpass":   "hunter2",
				"snmp_cryptopass": "hunter3",
				"snmp_authname":   "monitor",
			}}
			narrow(items, EntityDevices, v)
			for _, banned := range alwaysRemoved {
				if _, present := items[0][banned]; present {
					t.Errorf("%s survived the %s view", banned, v)
				}
			}
			if items[0]["hostname"] != "core-1" {
				t.Error("removing credentials took the hostname with it")
			}
		})
	}
}

// The full view is for the tool that promises one whole record, so it must
// keep the fields a summary drops -- everything except the credential.
func TestNarrow_FullViewKeepsWhatSummaryDrops(t *testing.T) {
	row := func() map[string]any {
		return map[string]any{
			"device_id": "1", "hostname": "core-1",
			"sysDescr": "a long banner", "poller_id": "0",
			"snmp_community": "public",
		}
	}

	summary := []map[string]any{row()}
	narrow(summary, EntityDevices, viewSummary)
	if _, present := summary[0]["sysDescr"]; present {
		t.Error("summary kept sysDescr")
	}

	full := []map[string]any{row()}
	narrow(full, EntityDevices, viewFull)
	if full[0]["sysDescr"] != "a long banner" {
		t.Error("full view dropped sysDescr")
	}
	if _, present := full[0]["snmp_community"]; present {
		t.Error("full view kept the community string")
	}
}

// An entity with no declared field set arrives whole rather than empty.
// Narrowing to a set nobody chose would be a quiet hole in the answer.
func TestNarrow_UnknownEntityIsNotEmptied(t *testing.T) {
	items := []map[string]any{{"bill_id": "3", "bill_name": "transit"}}
	kept, dropped := narrow(items, Entity("bills"), viewSummary)
	if len(items[0]) != 2 {
		t.Fatalf("kept %d fields, want 2 -- an undeclared entity must not be narrowed", len(items[0]))
	}
	if dropped != 0 {
		t.Errorf("reported %d dropped, want 0", dropped)
	}
	if len(kept) != 2 {
		t.Errorf("reported %d kept, want 2", len(kept))
	}
}

// The count is what a tool result uses to say it did not return everything.
func TestNarrow_ReportsWhatItDropped(t *testing.T) {
	items := []map[string]any{{
		"device_id": "1", "hostname": "core-1", "status": "1",
		"sysDescr": "x", "poller_id": "0", "location_geoapi": "arcgis",
	}}
	kept, dropped := narrow(items, EntityDevices, viewSummary)
	if len(kept) != 3 {
		t.Errorf("kept %v, want the three allowed fields", kept)
	}
	if dropped != 3 {
		t.Errorf("dropped = %d, want 3", dropped)
	}
}

// Read hands out copies, because a cached Page's maps are shared with every
// later reader of it. Narrowing them in place would empty the cache into
// whichever view was asked for first.
func TestRead_DoesNotNarrowTheCachedPage(t *testing.T) {
	var calls int
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"ok","count":1,"pagesize":250,"pageno":1,
			"devices":{"1":{"device_id":"1","hostname":"core-1","sysDescr":"banner","poller_id":"0"}}}`)
	})
	cfg := c.cfg
	cfg.StateCacheSeconds = 60
	cfg.InventoryCacheSeconds = 60
	c.cache = newReadCache("observium", cfg, c.now, nil)

	summary, err := c.Read(context.Background(), EntityDevices, url.Values{}, 0, viewSummary)
	if err != nil {
		t.Fatalf("summary read: %v", err)
	}
	if _, present := summary.Items[0]["sysDescr"]; present {
		t.Fatal("summary kept sysDescr")
	}

	full, err := c.Read(context.Background(), EntityDevices, url.Values{}, 0, viewFull)
	if err != nil {
		t.Fatalf("full read: %v", err)
	}
	if full.Items[0]["sysDescr"] != "banner" {
		t.Error("the full read was served the summary's narrowed maps out of the cache")
	}
	if calls != 1 {
		t.Errorf("made %d upstream calls, want 1 -- the second should be a cache hit", calls)
	}
}

// Credentials must not reach the cache at all, so that no later change to how
// views work can serve one out of storage.
func TestWalk_CredentialsNeverEnterTheCache(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"ok","count":1,"pagesize":250,"pageno":1,
			"devices":{"1":{"device_id":"1","hostname":"core-1","snmp_community":"public"}}}`)
	})

	page, err := c.walk(context.Background(), "/devices", "devices", url.Values{})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if _, present := page.Items[0]["snmp_community"]; present {
		t.Error("walk retained a community string; it is cached from here")
	}
}

// A listing that was narrowed has to say so, or a model reads a trimmed row as
// the whole record.
func TestResultOf_SaysWhenFieldsWereWithheld(t *testing.T) {
	page := Page{
		Items:         []map[string]any{{"device_id": "1", "hostname": "core-1"}},
		Fields:        []string{"device_id", "hostname"},
		FieldsDropped: 67,
	}
	out := resultOf(page, "devices")
	if out.Note == "" {
		t.Fatal("a narrowed listing said nothing about it")
	}
	if len(out.Fields) != 2 {
		t.Errorf("fields = %v, want the two that were kept", out.Fields)
	}
	blob, _ := json.Marshal(out)
	if !contains(string(blob), "67") {
		t.Errorf("the note does not say how many were withheld: %s", out.Note)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

// A limited call must not answer a later unlimited one. Before the ceiling was
// part of the cache key, `devices limit=5` stored a five-device page marked
// truncated, and the next caller asking for the estate was handed it back with
// advice to narrow a filter they had not set.
func TestReadCache_LimitedCallDoesNotPoisonTheEstate(t *testing.T) {
	var calls int
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"ok","count":3,"pagesize":250,"pageno":1,
			"devices":{"1":{"device_id":"1","hostname":"a"},
			           "2":{"device_id":"2","hostname":"b"},
			           "3":{"device_id":"3","hostname":"c"}}}`)
	})
	cfg := c.cfg
	cfg.StateCacheSeconds = 60
	cfg.InventoryCacheSeconds = 60
	c.cache = newReadCache("observium", cfg, c.now, nil)

	small, err := c.Read(context.Background(), EntityDevices, url.Values{}, 1, viewSummary)
	if err != nil {
		t.Fatalf("limited read: %v", err)
	}
	if len(small.Items) != 1 || !small.Truncated {
		t.Fatalf("limited read got %d items (truncated=%v), want 1 and truncated",
			len(small.Items), small.Truncated)
	}

	all, err := c.Read(context.Background(), EntityDevices, url.Values{}, 0, viewSummary)
	if err != nil {
		t.Fatalf("unlimited read: %v", err)
	}
	if len(all.Items) != 3 {
		t.Errorf("unlimited read got %d items, want 3 -- it was served the limited page", len(all.Items))
	}
	if all.Truncated {
		t.Error("unlimited read reported truncation it did not suffer")
	}
	if calls != 2 {
		t.Errorf("made %d upstream calls, want 2 -- the two ceilings are separate entries", calls)
	}
}

// The bug this exists for: seven of the twelve routes read a key named after
// their path, and Observium answers most of them under the generic "entries".
// decodeCollection returned no items and no error for a key that was not
// there, so capacity reported no processors against a host with thirty-seven
// and nothing anywhere said why.
func TestDecodeCollection_WrongKeyIsReportedNotSwallowed(t *testing.T) {
	body := []byte(`{"status":"ok","count":37,"entries":{"1":{"processor_id":"1"}}}`)

	_, err := decodeCollection(body, "processors")
	if err == nil {
		t.Fatal("a key that is not in the response was reported as an empty estate")
	}
	if !strings.Contains(err.Error(), `"entries"`) {
		t.Errorf("the error should name the key the response actually used: %v", err)
	}

	items, err := decodeCollection(body, "entries")
	if err != nil {
		t.Fatalf("the right key still has to work: %v", err)
	}
	if len(items) != 1 {
		t.Errorf("got %d items, want 1", len(items))
	}
}

// An estate that really is empty is not a wrong key, and must not be reported
// as one. Observium answers an empty collection as [] rather than {}.
func TestDecodeCollection_GenuinelyEmptyIsNotAnError(t *testing.T) {
	for _, body := range []string{
		`{"status":"ok","count":0,"entries":[]}`,
		`{"status":"ok","count":0}`,
	} {
		items, err := decodeCollection([]byte(body), "entries")
		if err != nil {
			t.Errorf("%s: reported an error for an empty result: %v", body, err)
		}
		if len(items) != 0 {
			t.Errorf("%s: got %d items, want 0", body, len(items))
		}
	}
}

// Every route's key has to be one this API actually uses. The table was
// written by naming keys after paths, which is how it came to be wrong.
func TestAPIPaths_KeysAreOnesObserviumUses(t *testing.T) {
	known := map[string]bool{
		"devices": true, "ports": true, "sensors": true, "alerts": true,
		"addresses": true, "storages": true, "entries": true, "bills": true,
		"power_bills": true, "counters": true, "groups": true, "statuses": true,
		"probes": true, "printersupplies": true, "alert_checks": true,
		"maintenance": true,
	}
	for entity, route := range apiPaths {
		if !known[route.key] {
			t.Errorf("%s reads %q, which is not a key this API answers under",
				entity, route.key)
		}
	}
}
