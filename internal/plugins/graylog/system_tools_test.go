package graylog

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// An input's configuration is whatever its plugin declares, and Graylog hands
// the whole map to any account that can read inputs. A syslog input has a
// port; an AWS input has a secret access key; a Beats input has a TLS key
// password. This is the only place that can be narrowed, and a credential in a
// tool result is a live credential in a model's context.
//
// The allow-list is the fix rather than a deny-list, for the reason the set is
// open-ended: a deny-list only ever covers the names somebody thought of.
func TestInputs_ReturnOnlyAllowListedSettings(t *testing.T) {
	p := toolPlugin(t, jsonOK(`{"total":1,"inputs":[{
		"id":"i1","title":"Beats","type":"org.graylog.plugins.beats.Beats2Input",
		"global":true,"node":"n1",
		"attributes":{
			"port":5044,"bind_address":"0.0.0.0","tls_enable":true,
			"tls_key_password":"hunter2",
			"aws_secret_access_key":"AKIAsecretsecret",
			"password":"letmein",
			"some_future_credential":"nope"}}]}`))

	got, err := p.getSystemStatus(context.Background(), systemArgs{Include: []string{sectionInputs}})
	if err != nil {
		t.Fatalf("system: %v", err)
	}
	if len(got.Inputs) != 1 {
		t.Fatalf("inputs = %d, want 1", len(got.Inputs))
	}

	raw, _ := json.Marshal(got)
	for _, secret := range []string{"hunter2", "AKIAsecretsecret", "letmein", "nope"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("a credential reached the tool result: %q", secret)
		}
	}
	settings := got.Inputs[0].Settings
	if settings["port"] == nil || settings["bind_address"] == nil {
		t.Errorf("the useful settings were dropped too: %v", settings)
	}
	if !strings.Contains(got.Note, "credentials") {
		t.Errorf("withholding should be stated rather than silent: %q", got.Note)
	}
}

// Yellow reads as "degraded, some data unavailable" and is not: it is the
// ordinary steady state of a single-node cluster with replicas configured.
// Treating it as an incident sends somebody looking for a fault that is a
// setting.
func TestHealth_SaysWhatTheColourCosts(t *testing.T) {
	p := toolPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case apiPrefix + "/system":
			_, _ = w.Write([]byte(`{"version":"7.1.0","node_id":"n1","is_processing":true}`))
		default:
			_, _ = w.Write([]byte(`{"status":"yellow","shards":{"active":10,"unassigned":5}}`))
		}
	})

	got, err := p.getSystemStatus(context.Background(), systemArgs{Include: []string{sectionHealth}})
	if err != nil {
		t.Fatalf("system: %v", err)
	}
	if got.Backend == nil || got.Backend.Status != "yellow" {
		t.Fatalf("backend = %+v, want the yellow status", got.Backend)
	}
	if !strings.Contains(got.Backend.Means, "single-node") {
		t.Errorf("yellow should say when it is normal: %q", got.Backend.Means)
	}
	if got.Server == nil || got.Server.Version != "7.1.0" {
		t.Errorf("server = %+v, want the version", got.Server)
	}
}

// A node that has stopped processing produces the same symptom as a bad query
// -- missing messages -- and does not look like an error from a search.
func TestHealth_FlagsANodeThatStoppedProcessing(t *testing.T) {
	p := toolPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == apiPrefix+"/system" {
			_, _ = w.Write([]byte(`{"version":"7.1.0","node_id":"n1","is_processing":false}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"green","shards":{}}`))
	})

	got, err := p.getSystemStatus(context.Background(), systemArgs{Include: []string{sectionHealth}})
	if err != nil {
		t.Fatalf("system: %v", err)
	}
	if !strings.Contains(got.Note, "not processing") {
		t.Errorf("a node that stopped indexing should be said out loud: %q", got.Note)
	}
}

// A misspelled section that silently returned the default would answer a
// different question than the one asked, with nothing saying so.
func TestSystem_RefusesAnUnknownSection(t *testing.T) {
	p := toolPlugin(t, jsonOK(`{}`))
	_, err := p.getSystemStatus(context.Background(), systemArgs{Include: []string{"heath"}})
	if err == nil {
		t.Fatal("a misspelled section was accepted")
	}
	if !strings.Contains(err.Error(), "index_sets") {
		t.Errorf("the refusal should list the sections, got: %v", err)
	}
}

// The default is the two sections that answer "is Graylog well". Six requests
// to answer a question two of them cover is a cost paid on every call.
func TestSystem_DefaultsToHealthAndNotifications(t *testing.T) {
	seen := map[string]bool{}
	p := toolPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		seen[r.URL.Path] = true
		switch r.URL.Path {
		case apiPrefix + "/system":
			_, _ = w.Write([]byte(`{"version":"7.1.0","node_id":"n1","is_processing":true}`))
		case apiPrefix + "/system/notifications":
			_, _ = w.Write([]byte(`{"total":0,"notifications":[]}`))
		default:
			_, _ = w.Write([]byte(`{"status":"green","shards":{}}`))
		}
	})

	if _, err := p.getSystemStatus(context.Background(), systemArgs{}); err != nil {
		t.Fatalf("system: %v", err)
	}
	for _, unwanted := range []string{"/system/inputs", "/system/cluster/nodes",
		"/system/indices/index_sets"} {
		if seen[apiPrefix+unwanted] {
			t.Errorf("%s was fetched without being asked for", unwanted)
		}
	}
}

// The failure mode this exists to prevent is a wrong collection key returning
// no items and no error, so the tool above reports that there are none. An
// absent key is an error, and the message names what the response did carry
// because that is the fix.
func TestPickList_NamesWhatItFoundInstead(t *testing.T) {
	_, _, err := pickList(json.RawMessage(`{"total":2,"elements":[{},{}]}`), "streams")
	if err == nil {
		t.Fatal("a response with no matching key was read as empty")
	}
	if !strings.Contains(err.Error(), "elements") {
		t.Errorf("the message should name the keys that were there, got: %v", err)
	}
}

// Graylog answers listings in three shapes depending on the endpoint's age.
func TestPickList_AcceptsTheShapesGraylogUses(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		candidates []string
		want       int
	}{
		{"bare array", `[{"name":"a"},{"name":"b"}]`, []string{"fields"}, 2},
		{"named collection", `{"total":7,"streams":[{},{}]}`, []string{"streams"}, 7},
		{"paginated envelope", `{"total":7,"elements":[{},{}]}`,
			[]string{"streams", "elements"}, 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items, total, err := pickList(json.RawMessage(tc.body), tc.candidates...)
			if err != nil {
				t.Fatalf("pickList: %v", err)
			}
			if total != tc.want {
				t.Errorf("total = %d, want %d", total, tc.want)
			}
			var decoded []json.RawMessage
			if err := json.Unmarshal(items, &decoded); err != nil {
				t.Fatalf("the returned value was not an array: %v", err)
			}
		})
	}
}

// A query naming a field that does not exist matches nothing and reports no
// error, which reads exactly like an all-clear. So an empty field search says
// what it means.
func TestFields_EmptyMatchSaysWhyItMatters(t *testing.T) {
	p := toolPlugin(t, jsonOK(`[{"name":"source","type":{"type":"string"}}]`))

	got, err := p.listMessageFields(context.Background(), fieldsArgs{Contains: "http_stauts"})
	if err != nil {
		t.Fatalf("fields: %v", err)
	}
	if got.Returned != 0 {
		t.Fatalf("returned = %d, want 0", got.Returned)
	}
	if !strings.Contains(got.Note, "all-clear") {
		t.Errorf("the note should say why a missing field is dangerous: %q", got.Note)
	}
}

// The API answers with a set, which has no order. Without sorting, the same
// call returns the same fields in a different sequence each time and a model
// comparing two answers sees changes that did not happen.
func TestFields_AreSorted(t *testing.T) {
	p := toolPlugin(t, jsonOK(`[
		{"name":"zulu","type":{"type":"string"}},
		{"name":"alpha","type":{"type":"long"}},
		{"name":"mike","type":{"type":"string"}}]`))

	got, err := p.listMessageFields(context.Background(), fieldsArgs{})
	if err != nil {
		t.Fatalf("fields: %v", err)
	}
	for i := 1; i < len(got.Fields); i++ {
		if got.Fields[i-1].Name > got.Fields[i].Name {
			t.Fatalf("fields are not sorted: %v", got.Fields)
		}
	}
}

// Naming streams is what cuts a field list from thousands to the ones that
// matter, and it is a different endpoint. A tool that quietly ignored the
// streams would answer a much larger question than the one asked.
func TestFields_NarrowingByStreamUsesThePostEndpoint(t *testing.T) {
	var method string
	p := toolPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		_, _ = w.Write([]byte(`[]`))
	})

	if _, err := p.listMessageFields(context.Background(), fieldsArgs{Streams: []string{"s1"}}); err != nil {
		t.Fatalf("fields: %v", err)
	}
	if method != http.MethodPost {
		t.Errorf("method = %s, want POST when streams are named", method)
	}
}

// A stopped stream still holds what it collected before it was stopped, so an
// empty search against one is not evidence of anything. Saying which streams
// are stopped is what stops that being read as an all-clear.
func TestStreams_SaysWhichAreStopped(t *testing.T) {
	p := toolPlugin(t, jsonOK(`{"total":2,"streams":[
		{"id":"s1","title":"Application logs","disabled":false,"rules":[{},{}]},
		{"id":"s2","title":"Old app","disabled":true,"rules":[]}]}`))

	got, err := p.listStreams(context.Background(), streamsArgs{})
	if err != nil {
		t.Fatalf("streams: %v", err)
	}
	if got.Streams[0].Enabled != true || got.Streams[1].Enabled != false {
		t.Errorf("disabled did not become enabled: %+v", got.Streams)
	}
	if got.Streams[0].Rules != 2 {
		t.Errorf("routing_rules = %d, want 2", got.Streams[0].Rules)
	}
	if !strings.Contains(got.Note, "stopped") {
		t.Errorf("a stopped stream should be called out: %q", got.Note)
	}
}

// A package path with one word of information in it costs a line to say "time
// based".
func TestSimpleClassName(t *testing.T) {
	got := simpleClassName("org.graylog2.indexer.rotation.strategies.TimeBasedRotationStrategyConfig")
	if got != "TimeBasedRotationStrategy" {
		t.Errorf("simpleClassName = %q", got)
	}
}
