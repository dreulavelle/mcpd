package bandwidth

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	"github.com/spoked/mcpd/internal/plugins"
)

// What has happened on the voice side.

func (p *Plugin) registerVoiceTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_calls",
		Title: "List calls",
		Description: "Calls on this account, most recent first. Narrow by the " +
			"number called or calling, by when they started, or by how they " +
			"ended — an unfiltered listing is truncated on any busy account. " +
			"Bandwidth keeps completed calls for a limited window, so an old " +
			"call may be absent rather than missing.",
		Idempotent: true,
	}, p.listCalls)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_call",
		Title: "Get one call",
		Description: "Everything known about one call: how it was set up, how " +
			"it ended, and what it cost in time. Use this when a question is " +
			"about a specific call rather than a pattern across many.",
		Idempotent: true,
	}, p.getCall)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_conferences",
		Title: "List conferences",
		Description: "Conferences on this account, optionally by name or by " +
			"when they were created.",
		Idempotent: true,
	}, p.listConferences)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_conference",
		Title: "Get one conference",
		Description: "One conference, and optionally one member of it. Ask for " +
			"a member when the question is why a particular participant could " +
			"not hear or be heard.",
		Idempotent: true,
	}, p.getConference)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_recordings",
		Title: "List recordings",
		Description: "Recordings across the account, or the recordings of one " +
			"call or one conference. Metadata only: this returns when a " +
			"recording was made and how long it is, never its audio.",
		Idempotent: true,
	}, p.listRecordings)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_recording",
		Title: "Get one recording",
		Description: "One recording's details, and its transcription text when " +
			"one was made and asked for. The audio itself is never returned.",
		Idempotent: true,
	}, p.getRecording)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_transcriptions",
		Title: "List live transcriptions for a call",
		Description: "Real-time transcriptions captured during one call, which " +
			"are separate from a recording's transcription. Ask for one by id " +
			"to get its text.",
		Idempotent: true,
	}, p.listTranscriptions)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_statistics",
		Title: "Get account call statistics",
		Description: "How much of this account's concurrent-call capacity is in " +
			"use right now. The tool to reach for when calls are being " +
			"rejected and nobody knows why.",
		Idempotent: true,
	}, p.getStatistics)
}

// CallsInput narrows a listing of calls.
type CallsInput struct {
	To              string `json:"to,omitempty" jsonschema:"the number that was called, in E.164 such as +19195551234"`
	From            string `json:"from,omitempty" jsonschema:"the number that placed the call, in E.164"`
	StartedAfter    string `json:"started_after,omitempty" jsonschema:"earliest call start, as an ISO-8601 instant such as 2026-08-31T00:00:00Z"`
	StartedBefore   string `json:"started_before,omitempty" jsonschema:"latest call start, as an ISO-8601 instant"`
	DisconnectCause string `json:"disconnect_cause,omitempty" jsonschema:"how the call ended, such as hangup busy timeout cancel rejected or error"`
	PageToken       string `json:"page_token,omitempty" jsonschema:"continue a previous listing from its next_page_token"`
	Limit           int    `json:"limit,omitempty" jsonschema:"most calls to return; the configured ceiling applies whatever this says"`
}

// Listing is a page of records from one of Bandwidth's collections.
//
// Deliberately flat and shared. The output schema is the largest single
// context cost a plugin carries, and one shape reused across every listing
// here is paid for once rather than nine times.
type Listing struct {
	Items    []Record `json:"items"`
	Returned int      `json:"returned"`
	// NextPageToken is present when Bandwidth said there is more. Empty means
	// this is the whole answer, which is different from not knowing.
	NextPageToken string `json:"next_page_token,omitempty"`
	// Note carries what the caller should know about the answer itself --
	// that it was capped, or that a filter was ignored.
	Note string `json:"note,omitempty"`
}

// Record is one upstream object, passed through as it arrived.
//
// Not modelled field by field, and that is the house style for a reason: a
// typed struct per resource would trade a large permanent schema cost for a
// tidiness a model does not read. See docs/plugins.md.
type Record = map[string]any

func (p *Plugin) listCalls(ctx context.Context, in CallsInput) (Listing, error) {
	if err := p.ready(); err != nil {
		return Listing{}, err
	}
	q := url.Values{}
	set(q, "to", in.To)
	set(q, "from", in.From)
	set(q, "minStartTime", in.StartedAfter)
	set(q, "maxStartTime", in.StartedBefore)
	set(q, "disconnectCause", in.DisconnectCause)
	set(q, "pageToken", in.PageToken)
	limit := p.client.limit(in.Limit)
	q.Set("pageSize", strconv.Itoa(limit))

	var items []Record
	err := p.client.get(ctx, hostVoice,
		fmt.Sprintf("/api/v2/accounts/%s/calls", p.client.AccountID()), q, &items)
	p.note(err, nil)
	if err != nil {
		return Listing{}, err
	}
	return capped(items, limit), nil
}

// CallInput names one call.
type CallInput struct {
	CallID string `json:"call_id" jsonschema:"the call id, as returned by list_calls"`
}

func (p *Plugin) getCall(ctx context.Context, in CallInput) (Record, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	if in.CallID == "" {
		return nil, fmt.Errorf("bandwidth: a call id is required")
	}
	var out Record
	err := p.client.get(ctx, hostVoice,
		fmt.Sprintf("/api/v2/accounts/%s/calls/%s", p.client.AccountID(),
			url.PathEscape(in.CallID)), nil, &out)
	p.note(err, nil)
	return out, err
}

// ConferencesInput narrows a listing of conferences.
type ConferencesInput struct {
	Name          string `json:"name,omitempty" jsonschema:"exact conference name"`
	CreatedAfter  string `json:"created_after,omitempty" jsonschema:"earliest creation time, as an ISO-8601 instant"`
	CreatedBefore string `json:"created_before,omitempty" jsonschema:"latest creation time, as an ISO-8601 instant"`
	PageToken     string `json:"page_token,omitempty" jsonschema:"continue a previous listing from its next_page_token"`
	Limit         int    `json:"limit,omitempty" jsonschema:"most conferences to return; the configured ceiling applies whatever this says"`
}

func (p *Plugin) listConferences(ctx context.Context, in ConferencesInput) (Listing, error) {
	if err := p.ready(); err != nil {
		return Listing{}, err
	}
	q := url.Values{}
	set(q, "name", in.Name)
	set(q, "minCreatedTime", in.CreatedAfter)
	set(q, "maxCreatedTime", in.CreatedBefore)
	set(q, "pageToken", in.PageToken)
	limit := p.client.limit(in.Limit)
	q.Set("pageSize", strconv.Itoa(limit))

	var items []Record
	err := p.client.get(ctx, hostVoice,
		fmt.Sprintf("/api/v2/accounts/%s/conferences", p.client.AccountID()), q, &items)
	p.note(err, nil)
	if err != nil {
		return Listing{}, err
	}
	return capped(items, limit), nil
}

// ConferenceInput names one conference, and optionally one member of it.
type ConferenceInput struct {
	ConferenceID string `json:"conference_id" jsonschema:"the conference id, as returned by list_conferences"`
	MemberID     string `json:"member_id,omitempty" jsonschema:"one member of the conference, to read that participant instead of the whole conference"`
}

func (p *Plugin) getConference(ctx context.Context, in ConferenceInput) (Record, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	if in.ConferenceID == "" {
		return nil, fmt.Errorf("bandwidth: a conference id is required")
	}
	path := fmt.Sprintf("/api/v2/accounts/%s/conferences/%s",
		p.client.AccountID(), url.PathEscape(in.ConferenceID))
	if in.MemberID != "" {
		path += "/members/" + url.PathEscape(in.MemberID)
	}
	var out Record
	err := p.client.get(ctx, hostVoice, path, nil, &out)
	p.note(err, nil)
	return out, err
}

// RecordingsInput selects whose recordings to list.
type RecordingsInput struct {
	CallID        string `json:"call_id,omitempty" jsonschema:"list only this call's recordings"`
	ConferenceID  string `json:"conference_id,omitempty" jsonschema:"list only this conference's recordings"`
	To            string `json:"to,omitempty" jsonschema:"the number that was called, in E.164; account-wide listings only"`
	From          string `json:"from,omitempty" jsonschema:"the number that placed the call, in E.164; account-wide listings only"`
	StartedAfter  string `json:"started_after,omitempty" jsonschema:"earliest recording start, as an ISO-8601 instant"`
	StartedBefore string `json:"started_before,omitempty" jsonschema:"latest recording start, as an ISO-8601 instant"`
	Limit         int    `json:"limit,omitempty" jsonschema:"most recordings to return; the configured ceiling applies whatever this says"`
}

func (p *Plugin) listRecordings(ctx context.Context, in RecordingsInput) (Listing, error) {
	if err := p.ready(); err != nil {
		return Listing{}, err
	}
	if in.CallID != "" && in.ConferenceID != "" {
		return Listing{}, fmt.Errorf("bandwidth: give a call id or a conference " +
			"id, not both — they are different collections")
	}

	account := p.client.AccountID()
	var path string
	q := url.Values{}
	switch {
	case in.CallID != "":
		path = fmt.Sprintf("/api/v2/accounts/%s/calls/%s/recordings",
			account, url.PathEscape(in.CallID))
	case in.ConferenceID != "":
		path = fmt.Sprintf("/api/v2/accounts/%s/conferences/%s/recordings",
			account, url.PathEscape(in.ConferenceID))
	default:
		path = fmt.Sprintf("/api/v2/accounts/%s/recordings", account)
		set(q, "to", in.To)
		set(q, "from", in.From)
		set(q, "minStartTime", in.StartedAfter)
		set(q, "maxStartTime", in.StartedBefore)
	}

	var items []Record
	err := p.client.get(ctx, hostVoice, path, q, &items)
	p.note(err, nil)
	if err != nil {
		return Listing{}, err
	}
	out := capped(items, p.client.limit(in.Limit))
	if in.CallID == "" && in.ConferenceID == "" && (in.To != "" || in.From != "") {
		return out, nil
	}
	return out, nil
}

// RecordingInput names one recording.
type RecordingInput struct {
	CallID            string `json:"call_id" jsonschema:"the call the recording belongs to"`
	RecordingID       string `json:"recording_id" jsonschema:"the recording id, as returned by list_recordings"`
	WithTranscription bool   `json:"with_transcription,omitempty" jsonschema:"also fetch the transcription text, when one was made"`
}

// RecordingOutput is one recording, and its transcription when asked for.
type RecordingOutput struct {
	Recording Record `json:"recording"`
	// Transcription is present only when it was asked for and exists. A
	// recording with no transcription is not an error: transcription is opt-in
	// per call, so its absence is the ordinary case.
	Transcription Record `json:"transcription,omitempty"`
	Note          string `json:"note,omitempty"`
}

func (p *Plugin) getRecording(ctx context.Context, in RecordingInput) (RecordingOutput, error) {
	if err := p.ready(); err != nil {
		return RecordingOutput{}, err
	}
	if in.CallID == "" || in.RecordingID == "" {
		return RecordingOutput{}, fmt.Errorf("bandwidth: both a call id and a " +
			"recording id are required")
	}
	base := fmt.Sprintf("/api/v2/accounts/%s/calls/%s/recordings/%s",
		p.client.AccountID(), url.PathEscape(in.CallID), url.PathEscape(in.RecordingID))

	var out RecordingOutput
	if err := p.client.get(ctx, hostVoice, base, nil, &out.Recording); err != nil {
		p.note(err, nil)
		return RecordingOutput{}, err
	}
	p.note(nil, nil)

	if !in.WithTranscription {
		return out, nil
	}
	// Best effort, and named when it fails. A missing transcription is the
	// ordinary case rather than a failure, so it must not turn a recording
	// that was read successfully into an error.
	var t Record
	if err := p.client.get(ctx, hostVoice, base+"/transcription", nil, &t); err != nil {
		out.Note = "the recording was read; its transcription was not: " + err.Error()
		return out, nil
	}
	out.Transcription = t
	return out, nil
}

// TranscriptionsInput names the call whose live transcriptions to read.
type TranscriptionsInput struct {
	CallID          string `json:"call_id" jsonschema:"the call id, as returned by list_calls"`
	TranscriptionID string `json:"transcription_id,omitempty" jsonschema:"one transcription, to return its text rather than the list"`
}

func (p *Plugin) listTranscriptions(ctx context.Context, in TranscriptionsInput) (Listing, error) {
	if err := p.ready(); err != nil {
		return Listing{}, err
	}
	if in.CallID == "" {
		return Listing{}, fmt.Errorf("bandwidth: a call id is required")
	}
	path := fmt.Sprintf("/api/v2/accounts/%s/calls/%s/transcriptions",
		p.client.AccountID(), url.PathEscape(in.CallID))

	if in.TranscriptionID != "" {
		var one Record
		err := p.client.get(ctx, hostVoice,
			path+"/"+url.PathEscape(in.TranscriptionID), nil, &one)
		p.note(err, nil)
		if err != nil {
			return Listing{}, err
		}
		return Listing{Items: []Record{one}, Returned: 1}, nil
	}

	var items []Record
	err := p.client.get(ctx, hostVoice, path, nil, &items)
	p.note(err, nil)
	if err != nil {
		return Listing{}, err
	}
	return capped(items, p.client.limit(0)), nil
}

// StatisticsInput takes nothing. It exists so the tool has a schema.
type StatisticsInput struct{}

func (p *Plugin) getStatistics(ctx context.Context, _ StatisticsInput) (Record, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	var out Record
	err := p.client.get(ctx, hostVoice,
		fmt.Sprintf("/api/v2/accounts/%s/statistics", p.client.AccountID()), nil, &out)
	p.note(err, nil)
	return out, err
}

// set adds a query parameter only when it has a value, so an omitted filter is
// absent rather than sent as an empty string -- which some of Bandwidth's
// endpoints treat as a filter matching nothing.
func set(q url.Values, key, value string) {
	if value != "" {
		q.Set(key, value)
	}
}

// capped truncates a listing to the ceiling and says so.
//
// Saying so is the point. A caller handed the first two hundred of nine
// hundred calls, with nothing to indicate it, will reason about the estate as
// though it were two hundred calls.
func capped(items []Record, limit int) Listing {
	out := Listing{Items: items, Returned: len(items)}
	if limit > 0 && len(items) > limit {
		out.Items = items[:limit]
		out.Returned = limit
		out.Note = fmt.Sprintf("truncated to %d of %d; narrow the filters or "+
			"raise the ceiling in settings", limit, len(items))
	}
	return out
}
