package extremecloudiq

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// The connection trail is the only place this API says *which step* failed
// rather than that something did, so the tool has to reach it and the result
// has to point at the fields that carry the verdict.
func TestGetClientHistory_ComposesTheTrail(t *testing.T) {
	var reached []string
	p := toolPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		reached = append(reached, r.URL.Path)
		switch {
		case strings.HasPrefix(r.URL.Path, "/clients/byMac/"):
			_, _ = io.WriteString(w, `{"id":88,"mac_address":"AA:BB:CC:DD:EE:FF"}`)
		case strings.Contains(r.URL.Path, "connectivity-experience"):
			_, _ = io.WriteString(w, `{"data":[{"start_timestamp":1,`+
				`"authentication_status":"FAILED","auth_circle_status":"RED",`+
				`"dhcp_circle_status":"GREEN","avg_rssi":-71}]}`)
		case strings.Contains(r.URL.Path, "roaming-trail"):
			_, _ = io.WriteString(w, `{"data":[{"device_name_from":"ap-1",`+
				`"device_name_to":"ap-2","roam_duration":180}]}`)
		default:
			_, _ = io.WriteString(w, `{"ssid":"corp","vlan":30}`)
		}
	})

	out, err := p.getClientHistory(context.Background(),
		ClientHistoryInput{Client: "AA:BB:CC:DD:EE:FF"})
	if err != nil {
		t.Fatalf("getClientHistory: %v", err)
	}
	if out.ClientID != 88 {
		t.Errorf("ClientID = %d; the MAC was not resolved", out.ClientID)
	}
	if len(out.Attempts) != 1 || out.Attempts[0]["authentication_status"] != "FAILED" {
		t.Errorf("attempts = %v", out.Attempts)
	}
	if len(out.Roams) != 1 {
		t.Errorf("roams = %v", out.Roams)
	}
	if !strings.Contains(out.Note, "circle_status") {
		t.Errorf("the note does not point at the per-stage verdicts: %q", out.Note)
	}
	if out.Window == "" {
		t.Error("no window was reported")
	}
	// Three reads plus the resolution, and nothing that was not asked for.
	if len(reached) != 4 {
		t.Errorf("made %d requests: %v", len(reached), reached)
	}
}

// Most phones randomise their MAC per network, so "the address on the device's
// settings screen" is routinely not the one it connected with. That is the
// first thing somebody should be told when a lookup finds nothing, because it
// is the usual cause and it does not look like one.
func TestClientID_ExplainsARandomisedMAC(t *testing.T) {
	p := toolPlugin(t, jsonOK(`{}`))
	_, err := p.clientID(context.Background(), "AA:BB:CC:DD:EE:FF")
	if err == nil {
		t.Fatal("a MAC matching nothing was accepted")
	}
	if !strings.Contains(err.Error(), "randomise") {
		t.Errorf("the message does not mention MAC randomisation: %v", err)
	}
}

// A client that connected before the window opened has no attempts in it, and
// that is a different thing from a client with a problem. An empty result that
// said nothing would be read as the latter.
func TestGetClientHistory_DistinguishesQuietFromAbsent(t *testing.T) {
	p := toolPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/clients/byMac/"):
			_, _ = io.WriteString(w, `{"id":88}`)
		case strings.Contains(r.URL.Path, "client-trail"):
			_, _ = io.WriteString(w, `{"data":[]}`)
		default:
			_, _ = io.WriteString(w, `{"ssid":"corp"}`)
		}
	})

	out, err := p.getClientHistory(context.Background(), ClientHistoryInput{Client: "AA:BB:CC:DD:EE:FF"})
	if err != nil {
		t.Fatalf("getClientHistory: %v", err)
	}
	if !strings.Contains(out.Note, "widen the window") {
		t.Errorf("a client with details but no attempts was not explained: %q", out.Note)
	}
}

// Copilot is a licensed feature. An account without it fails in a way that
// reads as a broken integration, and saying which it is saves an afternoon.
func TestListAnomalies_NamesTheLicenceAsAPossibility(t *testing.T) {
	p := toolPlugin(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error_message":"not permitted"}`)
	})
	_, err := p.listAnomalies(context.Background(), AnomaliesInput{})
	if err == nil {
		t.Fatal("a refused anomaly read reported success")
	}
	if !strings.Contains(err.Error(), "Copilot") {
		t.Errorf("the message does not mention the licence: %v", err)
	}
}

// Muted anomalies are ones somebody has already decided not to care about, and
// including them turns "what needs attention" into "what has ever been
// noticed".
func TestListAnomalies_LeavesOutWhatSomebodyMuted(t *testing.T) {
	var muted string
	p := toolPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		muted = r.URL.Query().Get("excludeMuted")
		_, _ = io.WriteString(w, `{"anomalies_by_type":[{"name":"POE_FLAPPING","count":3}]}`)
	})
	out, err := p.listAnomalies(context.Background(), AnomaliesInput{})
	if err != nil {
		t.Fatalf("listAnomalies: %v", err)
	}
	if muted != "true" {
		t.Errorf("excludeMuted = %q", muted)
	}
	if len(out.ByKind) != 1 {
		t.Errorf("by_kind = %v", out.ByKind)
	}
	// And it must not be confused with the alert list, which is rules somebody
	// wrote rather than patterns the platform found.
	if !strings.Contains(out.Note, "list_alerts") {
		t.Errorf("the note does not distinguish these from alerts: %q", out.Note)
	}
}
