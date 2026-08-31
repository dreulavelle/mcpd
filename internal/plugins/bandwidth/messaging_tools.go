package bandwidth

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/plugins"
)

// defaultMessageWindow is how far back an otherwise unfiltered message search
// reaches. The API requires at least one filter and this is the least
// surprising one to supply.
const defaultMessageWindow = 24 * time.Hour

// What has been sent and received.

func (p *Plugin) registerMessagingTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "search_messages",
		Title: "Search messages",
		Description: "SMS and MMS on this account, by number, direction, " +
			"status, carrier, campaign or time. This is the tool for “did that " +
			"text arrive”: message_status and error_code together say whether " +
			"it was delivered, is still in flight, or was rejected and by " +
			"whom. Message bodies are not returned — Bandwidth does not store " +
			"them.",
		Idempotent: true,
	}, p.searchMessages)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_media",
		Title: "List stored media",
		Description: "Media files stored on this account for MMS. Metadata " +
			"only: names and sizes, never the file contents.",
		Idempotent: true,
	}, p.listMedia)
}

// MessagesInput narrows a message search.
//
// A subset of what Bandwidth offers. The full parameter list runs past twenty
// and most of it answers questions about carrier latency that a model asking
// "did this text arrive" does not have; every one carried here is charged to
// the context of every conversation, used or not.
type MessagesInput struct {
	Account     string `json:"account,omitempty" jsonschema:"account number to read; omit for the default account"`
	MessageID   string `json:"message_id,omitempty" jsonschema:"one message by its id"`
	From        string `json:"from,omitempty" jsonschema:"the sending number, in E.164 such as +19195551234"`
	To          string `json:"to,omitempty" jsonschema:"the receiving number, in E.164"`
	Direction   string `json:"direction,omitempty" jsonschema:"inbound or outbound"`
	Status      string `json:"status,omitempty" jsonschema:"delivery status such as received delivered sending failed or accepted"`
	ErrorCode   string `json:"error_code,omitempty" jsonschema:"Bandwidth error code, to find only messages that failed a particular way"`
	Carrier     string `json:"carrier,omitempty" jsonschema:"carrier name, to find messages handled by one carrier"`
	CampaignID  string `json:"campaign_id,omitempty" jsonschema:"10DLC campaign id, to find traffic sent under one campaign"`
	MessageType string `json:"message_type,omitempty" jsonschema:"sms or mms"`
	SentAfter   string `json:"sent_after,omitempty" jsonschema:"earliest message time, as an ISO-8601 instant such as 2026-08-31T00:00:00Z"`
	SentBefore  string `json:"sent_before,omitempty" jsonschema:"latest message time, as an ISO-8601 instant"`
	Sort        string `json:"sort,omitempty" jsonschema:"a field and direction, such as sourceTn:desc"`
	PageToken   string `json:"page_token,omitempty" jsonschema:"continue a previous search from its next_page_token"`
	Limit       int    `json:"limit,omitempty" jsonschema:"most messages to return; the configured ceiling applies whatever this says"`
}

// messagesEnvelope is how the messaging API returns a page.
//
// Unlike the voice endpoints, which answer with a bare array, this one wraps
// the rows and carries the paging token beside them.
type messagesEnvelope struct {
	TotalCount int `json:"totalCount"`
	PageInfo   struct {
		NextPageToken string `json:"nextPageToken"`
	} `json:"pageInfo"`
	Messages []Record `json:"messages"`
}

func (p *Plugin) searchMessages(ctx context.Context, in MessagesInput) (Listing, error) {
	if err := p.ready(); err != nil {
		return Listing{}, err
	}
	account, err := p.client.resolveAccount(ctx, in.Account)
	if err != nil {
		return Listing{}, err
	}
	q := url.Values{}
	set(q, "messageId", in.MessageID)
	set(q, "sourceTn", in.From)
	set(q, "destinationTn", in.To)
	set(q, "messageDirection", in.Direction)
	set(q, "messageStatus", in.Status)
	set(q, "errorCode", in.ErrorCode)
	set(q, "carrierName", in.Carrier)
	set(q, "campaignId", in.CampaignID)
	set(q, "messageType", in.MessageType)
	set(q, "fromDateTime", normaliseMessagingTime(in.SentAfter))
	set(q, "toDateTime", normaliseMessagingTime(in.SentBefore))
	set(q, "sort", in.Sort)
	set(q, "pageToken", in.PageToken)
	limit := p.client.limit(in.Limit)
	q.Set("limit", strconv.Itoa(limit))

	// The messaging API refuses a search with no filter at all -- "Missing
	// search params, requires one of ..." -- so an unqualified "show me
	// messages" fails rather than returning recent ones. A window is supplied
	// when nothing else narrows it, because the caller's question is almost
	// always "lately", and the answer says a window was chosen so that nobody
	// reads it as the whole history.
	var note string
	if len(q) == 1 {
		q.Set("fromDateTime", messagingTime(p.deps.Now().Add(-defaultMessageWindow)))
		note = "no filter was given, so this covers the last 24 hours only — " +
			"narrow by number, status or date to look further back"
	}

	var env messagesEnvelope
	// The messaging API says users where the voice API says accounts, for the
	// same id. Bandwidth's history, not a mistake here.
	err = p.client.get(ctx, hostMessaging,
		fmt.Sprintf("/api/v2/users/%s/messages", account), q, &env)
	p.note(err, nil)
	if err != nil {
		return Listing{}, err
	}

	out := capped(env.Messages, limit)
	out.NextPageToken = env.PageInfo.NextPageToken
	if out.Note == "" {
		out.Note = note
	}
	if env.TotalCount > out.Returned && out.Note == "" {
		out.Note = fmt.Sprintf("%d matched; %d returned. Use page_token to "+
			"continue.", env.TotalCount, out.Returned)
	}
	return out, nil
}

// MediaInput continues a media listing.
type MediaInput struct {
	Account           string `json:"account,omitempty" jsonschema:"account number to read; omit for the default account"`
	ContinuationToken string `json:"continuation_token,omitempty" jsonschema:"continue a previous listing from its next_page_token"`
}

func (p *Plugin) listMedia(ctx context.Context, in MediaInput) (Listing, error) {
	if err := p.ready(); err != nil {
		return Listing{}, err
	}
	account, err := p.client.resolveAccount(ctx, in.Account)
	if err != nil {
		return Listing{}, err
	}
	q := url.Values{}
	set(q, "continuationToken", in.ContinuationToken)

	var items []Record
	err = p.client.get(ctx, hostMessaging,
		fmt.Sprintf("/api/v2/users/%s/media", account), q, &items)
	p.note(err, nil)
	if err != nil {
		return Listing{}, err
	}
	return capped(items, p.client.limit(0)), nil
}

// messagingTime formats an instant the way the messaging API insists on.
//
// Milliseconds and a literal Z. RFC 3339 without them is refused --
// "'fromDateTime' has invalid date format, e.g valid format
// 2020-12-30T23:59:59.000Z" -- which is a distinction no caller should have to
// know and every caller would otherwise have to discover.
func messagingTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// normaliseMessagingTime rewrites whatever a caller supplied into that form.
//
// A model asked for "messages since yesterday" produces RFC 3339, because that
// is what every other tool here takes. Refusing it, or passing it through to be
// refused upstream, would make this one endpoint an exception for no reason a
// caller can see. Anything unparseable is passed through untouched so the API's
// own complaint reaches whoever wrote it.
func normaliseMessagingTime(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	for _, layout := range []string{
		"2006-01-02T15:04:05.000Z", time.RFC3339Nano, time.RFC3339,
		"2006-01-02T15:04:05", "2006-01-02",
	} {
		if t, err := time.Parse(layout, value); err == nil {
			return messagingTime(t)
		}
	}
	return value
}
