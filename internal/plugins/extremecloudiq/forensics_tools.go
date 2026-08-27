package extremecloudiq

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spoked/mcpd/internal/plugins"
)

// One client, in detail — and what the platform's own analysis has noticed.

func (p *Plugin) registerForensicsTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_client_history",
		Title: "Get one client's connection history",
		Description: "Why one device is having a bad time. Every connection " +
			"attempt in the window broken into its steps — association, " +
			"authentication, DHCP, DNS, gateway — with how long each took, " +
			"which one failed, and the signal at the time; plus every roam " +
			"between access points and how long it took. This is the tool for " +
			"“my laptop keeps dropping” and for “it takes forever to connect”, " +
			"which are different faults that look the same from the outside. " +
			"Name the client by MAC address.",
		Idempotent: true,
	}, p.getClientHistory)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_anomalies",
		Title: "List what the platform has flagged by itself",
		Description: "ExtremeCloud IQ's own analysis: anomalies it detected " +
			"without anybody asking, counted by site, by severity and by kind " +
			"— PoE flapping, missing VLANs, radar on a channel, port " +
			"mismatches, capacity trouble. Different from alerts, which fire " +
			"on rules somebody wrote; these are patterns the platform found. " +
			"Needs the Copilot feature on the account, and says so plainly if " +
			"it is not there.",
		Idempotent: true,
	}, p.listAnomalies)
}

// ClientHistoryInput names one client and a window.
type ClientHistoryInput struct {
	Client string `json:"client" jsonschema:"the client's MAC address, or its ExtremeCloud IQ id if you already have one"`
	timeArgs
	Limit int `json:"limit,omitempty" jsonschema:"most connection attempts and most roams to return"`
}

// ClientHistoryOutput is one client's experience over a window.
type ClientHistoryOutput struct {
	ClientID int64  `json:"client_id"`
	Window   string `json:"window"`
	// Client is who and what it is: the device it is on, its SSID, VLAN,
	// radio, channel and user profile.
	Client Record `json:"client,omitempty"`
	// Attempts are the connection attempts, each broken into its stages. This
	// is the part that says *which step* failed rather than that something
	// did.
	Attempts []Record `json:"connection_attempts,omitempty"`
	// Roams are the moves between access points, with how long each took.
	Roams    []Record `json:"roams,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	Note     string   `json:"note,omitempty"`
}

func (p *Plugin) getClientHistory(ctx context.Context, in ClientHistoryInput) (ClientHistoryOutput, error) {
	if err := p.ready(); err != nil {
		return ClientHistoryOutput{}, err
	}
	w, err := p.cfg.resolve(in.timeArgs, p.deps.Now())
	if err != nil {
		return ClientHistoryOutput{}, err
	}
	id, err := p.clientID(ctx, in.Client)
	if err != nil {
		return ClientHistoryOutput{}, err
	}

	out := ClientHistoryOutput{ClientID: id, Window: w.describe()}
	budget := plugins.ResultBudget(3)
	limit := p.limit(in.Limit)
	window := url.Values{}
	w.apply(window)
	base := "/client-details"
	who := strconv.FormatInt(id, 10)

	var client Record
	if err := p.client.GetInto(ctx, base+"/overview/info/"+who, window, &client); err != nil {
		out.Warnings = append(out.Warnings, "could not read this client's details: "+err.Error())
	} else if len(client) > 0 {
		out.Client = client
	}

	// The connection trail is the reason this tool exists. Each row is one
	// attempt with a status per stage, so a model can say "DHCP took nine
	// seconds" rather than "the client had a problem".
	var attempts struct {
		Data []Record `json:"data"`
	}
	if err := p.client.GetInto(ctx, base+"/client-trail/connectivity-experience/"+who,
		window, &attempts); err != nil {
		out.Warnings = append(out.Warnings, "could not read connection attempts: "+err.Error())
	} else {
		out.Attempts = capRows(attempts.Data, limit, budget)
	}

	var roams struct {
		Data []Record `json:"data"`
	}
	if err := p.client.GetInto(ctx, base+"/client-trail/roaming-trail/grid/"+who,
		window, &roams); err != nil {
		out.Warnings = append(out.Warnings, "could not read roaming history: "+err.Error())
	} else {
		out.Roams = capRows(roams.Data, limit, budget)
	}

	p.note(nil)
	switch {
	case len(out.Attempts) > 0:
		out.Note = "In each attempt the *_circle_status fields are the verdict " +
			"per stage — association, auth, dhcp, dns, gateway — and the " +
			"matching *_response_time fields say how long that stage took. A " +
			"stage that passed slowly is a different fault from one that failed."
	case len(out.Warnings) == 0 && len(out.Client) == 0:
		out.Note = "ExtremeCloud IQ holds nothing for this client in " +
			w.describe() + ". A client that has not connected in the window " +
			"looks like this; so does one whose MAC is randomised per network, " +
			"which most phones now do by default."
	default:
		out.Note = "No connection attempts were recorded in " + w.describe() +
			". A client that connected before the window opened and has stayed " +
			"connected since has nothing to show here — widen the window to " +
			"find the attempt that started the session."
	}
	return out, nil
}

// AnomaliesInput narrows the platform's own findings.
type AnomaliesInput struct {
	timeArgs
	Kind     string `json:"kind,omitempty" jsonschema:"one anomaly type to count, as named in a previous answer; omit for every kind"`
	Severity string `json:"severity,omitempty" jsonschema:"one severity to count; omit for every severity"`
}

// AnomaliesOutput is what the platform noticed by itself.
type AnomaliesOutput struct {
	Window string `json:"window"`
	// The three cuts the API offers, kept as it sent them. Which one is useful
	// depends on the question: by location to place a problem, by kind to
	// recognise it, by severity to decide whether it matters now.
	ByLocation []Record `json:"by_location,omitempty"`
	ByKind     []Record `json:"by_kind,omitempty"`
	BySeverity Record   `json:"by_severity,omitempty"`
	Note       string   `json:"note,omitempty"`
}

func (p *Plugin) listAnomalies(ctx context.Context, in AnomaliesInput) (AnomaliesOutput, error) {
	if err := p.ready(); err != nil {
		return AnomaliesOutput{}, err
	}
	w, err := p.cfg.resolve(in.timeArgs, p.deps.Now())
	if err != nil {
		return AnomaliesOutput{}, err
	}

	params := url.Values{}
	w.apply(params)
	setIf(params, "anomalyType", in.Kind)
	setIf(params, "severity", in.Severity)
	// Muted anomalies are ones somebody has already decided not to care about.
	// Excluding them is the difference between "what needs attention" and "what
	// the platform has ever noticed".
	params.Set("excludeMuted", "true")

	var body struct {
		ByLocation []Record `json:"anomalies_by_location"`
		BySeverity Record   `json:"anomalies_by_severity"`
		ByKind     []Record `json:"anomalies_by_type"`
	}
	if err := p.client.GetInto(ctx, "/copilot/anomalies/anomalies-by-category", params, &body); err != nil {
		p.note(err)
		// Copilot is a licensed feature, and an account without it answers
		// this in a way that reads as a broken integration rather than as an
		// absent product. Saying which it is saves somebody an afternoon.
		return AnomaliesOutput{}, fmt.Errorf("%w. If this account does not have "+
			"Copilot, that is what this looks like: the endpoint exists for "+
			"every tenant and answers only for the ones licensed for it", err)
	}
	p.note(nil)

	out := AnomaliesOutput{
		Window:     w.describe(),
		ByLocation: body.ByLocation,
		ByKind:     body.ByKind,
		BySeverity: body.BySeverity,
	}
	if len(out.ByLocation) == 0 && len(out.ByKind) == 0 && len(out.BySeverity) == 0 {
		out.Note = "Nothing was flagged in " + w.describe() + ". Muted " +
			"anomalies are left out, so a site somebody has already silenced " +
			"will not appear here."
	} else {
		out.Note = "These are patterns the platform found on its own, not " +
			"rules somebody wrote — use extremecloudiq_list_alerts for those. " +
			"Counts only: the platform's own interface is where an individual " +
			"anomaly is read."
	}
	return out, nil
}

// clientID resolves a MAC address into the id the client-detail endpoints take.
//
// A MAC is what somebody has: it is on the label, in the DHCP lease, in the
// ticket. The client-trail endpoints take a numeric id that appears nowhere a
// person would look, so one lookup here is the difference between a tool that
// can be called and one that cannot.
func (p *Plugin) clientID(ctx context.Context, named string) (int64, error) {
	name := strings.TrimSpace(named)
	if name == "" {
		return 0, fmt.Errorf("extremecloudiq: name a client by its MAC address")
	}
	// All digits is an id, on the same terms as a device: said in the tool
	// description, and the colliding shape -- a MAC typed without separators --
	// is a caller who should be told what happens rather than left to wonder.
	if id, err := strconv.ParseInt(name, 10, 64); err == nil && id > 0 {
		return id, nil
	}

	var client Record
	path := "/clients/byMac/" + url.PathEscape(name)
	if err := p.client.GetInto(ctx, path, url.Values{"views": {"BASIC"}}, &client); err != nil {
		p.note(err)
		return 0, fmt.Errorf("%w. The MAC is matched exactly, in the spelling "+
			"ExtremeCloud IQ stores -- colons rather than hyphens. A client "+
			"that has never connected here is absent rather than empty", err)
	}
	if len(client) == 0 {
		return 0, fmt.Errorf("extremecloudiq: no client here has the MAC %q. "+
			"Most phones randomise their MAC per network, so the address on a "+
			"device's own settings screen is often not the one it connected "+
			"with", name)
	}
	return recordID(client, name)
}
