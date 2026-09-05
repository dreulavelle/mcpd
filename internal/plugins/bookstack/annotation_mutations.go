package bookstack

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/spoked/mcpd/internal/operations"
	"github.com/spoked/mcpd/internal/plugins"
)

// The things hung off a page rather than written into it: comments and
// attachments.
//
// Both differ from page content in one way that decides their declarations:
// BookStack keeps no revision of either and no recycle bin entry for either,
// so deleting one destroys it. Every delete here says Reversible false, which
// is the honest answer and also stops a standing rule from ever approving one.

func (p *Plugin) commentMutations() []mutationEntry {
	return []mutationEntry{
		entry(plugins.MutationSpec{
			Action: "comment.create",
			Title:  "Comment on a page",
			Description: "Proposes adding a comment to a page, optionally as a reply " +
				"to another comment.",
			Risk: operations.RiskLow,
			// Deleting the comment removes it -- which is destructive for the
			// comment, but it is a way back from having posted one.
			Reversible: true,
			Verifiable: true,
		}, &commentCreate{p: p}),

		entry(plugins.MutationSpec{
			Action: "comment.update",
			Title:  "Edit or archive a comment",
			Description: "Proposes changing a comment's text, or archiving it so it " +
				"is folded away without being deleted.",
			Risk:       operations.RiskLow,
			Reversible: true,
			Verifiable: true,
		}, &commentUpdate{p: p}),

		entry(plugins.MutationSpec{
			Action: "comment.delete",
			Title:  "Delete a comment",
			Description: "Proposes removing a comment. It is destroyed rather than " +
				"sent to the recycle bin — archiving it instead keeps it.",
			Risk: operations.RiskMedium,
			// No revision, no recycle bin. Gone is gone.
			Reversible: false,
			Verifiable: true,
		}, &commentDelete{p: p}),
	}
}

func (p *Plugin) attachmentMutations() []mutationEntry {
	return []mutationEntry{
		entry(plugins.MutationSpec{
			Action: "attachment.create",
			Title:  "Attach a link to a page",
			Description: "Proposes attaching a link to a page. Uploading a file is " +
				"not something a tool call can carry, so this attaches links only.",
			Risk:       operations.RiskLow,
			Reversible: true,
			Verifiable: true,
		}, &attachmentCreate{p: p}),

		entry(plugins.MutationSpec{
			Action:      "attachment.update",
			Title:       "Update an attachment",
			Description: "Proposes renaming an attachment, changing its link, or moving it to another page.",
			Risk:        operations.RiskLow,
			Reversible:  true,
			Verifiable:  true,
		}, &attachmentUpdate{p: p}),

		entry(plugins.MutationSpec{
			Action: "attachment.delete",
			Title:  "Delete an attachment",
			Description: "Proposes removing an attachment from a page. An uploaded " +
				"file is destroyed with it and there is no recycle bin for one.",
			Risk: operations.RiskMedium,
			// An uploaded file has no copy anywhere once this runs.
			Reversible: false,
			Verifiable: true,
		}, &attachmentDelete{p: p}),
	}
}

// --- comments ---------------------------------------------------------------

// commentState is what the comment mutations observe.
type commentState struct {
	Exists   bool   `json:"exists"`
	ID       int    `json:"id,omitempty"`
	PageID   int    `json:"page_id,omitempty"`
	HTML     string `json:"html,omitempty"`
	Archived bool   `json:"archived,omitempty"`
	Updated  string `json:"updated_at,omitempty"`
}

// CommentCreateParams is a new comment.
type CommentCreateParams struct {
	PageID  int    `json:"page_id" jsonschema:"the page to comment on"`
	HTML    string `json:"html" jsonschema:"the comment's text, as HTML"`
	ReplyTo int    `json:"reply_to,omitempty" jsonschema:"the local id of the comment this replies to"`
}

type commentCreate struct{ p *Plugin }

func (h *commentCreate) Plan(ctx context.Context, params CommentCreateParams) (plugins.Plan[commentState], error) {
	var plan plugins.Plan[commentState]
	if err := h.p.mutationReady(); err != nil {
		return plan, err
	}
	if params.PageID <= 0 {
		return plan, fmt.Errorf("bookstack: a comment needs the page it goes on")
	}
	if strings.TrimSpace(params.HTML) == "" {
		return plan, fmt.Errorf("bookstack: a comment needs something to say")
	}
	pageState, err := h.p.readEntity(ctx, "pages", params.PageID)
	if err != nil {
		return plan, err
	}
	if !pageState.Exists {
		return plan, fmt.Errorf("bookstack: there is no page %d to comment on", params.PageID)
	}
	return plugins.Plan[commentState]{
		Before:        commentState{Exists: false, PageID: params.PageID},
		Desired:       commentState{Exists: true, PageID: params.PageID, HTML: params.HTML},
		Preconditions: map[string]any{"exists": false, "page_id": params.PageID},
		Changes: []operations.Change{
			{Field: "page", From: nil, To: pageState.Name},
			{Field: "comment", From: nil, To: forDiff(params.HTML)},
		},
		Impact: fmt.Sprintf("Adds a comment to %q, visible to everybody who can "+
			"see that page.", pageState.Name),
	}, nil
}

func (h *commentCreate) Apply(ctx context.Context, params CommentCreateParams, _ plugins.Plan[commentState]) (plugins.ApplyResult, error) {
	payload := map[string]any{"page_id": params.PageID, "html": params.HTML}
	if params.ReplyTo > 0 {
		payload["reply_to"] = params.ReplyTo
	}
	raw, err := h.p.client.send(ctx, "POST", "/api/comments", payload)
	h.p.noted(err)
	if err != nil {
		return plugins.ApplyResult{}, wrapIndeterminate(err)
	}
	return applied(raw)
}

// Observe looks for the comment on the page it was added to.
//
// BookStack's comment listing does not carry the text -- only a single read
// does -- so this takes the newest comment on the page and reads it, rather
// than trying to match text against a listing that has none.
func (h *commentCreate) Observe(ctx context.Context, params CommentCreateParams) (commentState, error) {
	rows, err := h.p.commentsOn(ctx, params.PageID)
	if err != nil {
		return commentState{}, err
	}
	newest := 0
	for _, c := range rows {
		if c.ID > newest {
			newest = c.ID
		}
	}
	if newest == 0 {
		return commentState{Exists: false, PageID: params.PageID}, nil
	}
	got, err := h.p.readComment(ctx, newest)
	if err != nil {
		return commentState{}, err
	}
	if got.HTML != params.HTML {
		// The newest comment is not the one this created, which means it did
		// not land. Saying so is better than reporting the wrong comment as
		// confirmation.
		return commentState{Exists: false, PageID: params.PageID}, nil
	}
	return got, nil
}

// CommentUpdateParams changes a comment.
type CommentUpdateParams struct {
	ID       int    `json:"id" jsonschema:"the comment's numeric id"`
	HTML     string `json:"html,omitempty" jsonschema:"replacement text as HTML"`
	Archived *bool  `json:"archived,omitempty" jsonschema:"true to fold the comment away, false to bring it back"`
}

type commentUpdate struct{ p *Plugin }

func (h *commentUpdate) Plan(ctx context.Context, params CommentUpdateParams) (plugins.Plan[commentState], error) {
	var plan plugins.Plan[commentState]
	if err := h.p.mutationReady(); err != nil {
		return plan, err
	}
	before, err := h.p.readComment(ctx, params.ID)
	if err != nil {
		return plan, err
	}
	desired := before
	changes := []operations.Change{}
	if body := strings.TrimSpace(params.HTML); body != "" && params.HTML != before.HTML {
		desired.HTML = params.HTML
		changes = diffText(changes, "comment", before.HTML, params.HTML)
	}
	if params.Archived != nil && *params.Archived != before.Archived {
		desired.Archived = *params.Archived
		changes = diffField(changes, "archived", before.Archived, *params.Archived)
	}
	if len(changes) == 0 {
		return plan, fmt.Errorf("bookstack: nothing to change on comment %d", params.ID)
	}
	return plugins.Plan[commentState]{
		Before:        before,
		Desired:       desired,
		Preconditions: map[string]any{"exists": true, "updated_at": before.Updated},
		Changes:       changes,
		Impact:        "Changes a comment on a page. Archiving folds it away without deleting it.",
		Rollback:      CommentUpdateParams{ID: params.ID, HTML: before.HTML, Archived: &before.Archived},
	}, nil
}

func (h *commentUpdate) Apply(ctx context.Context, params CommentUpdateParams, _ plugins.Plan[commentState]) (plugins.ApplyResult, error) {
	payload := map[string]any{}
	if strings.TrimSpace(params.HTML) != "" {
		payload["html"] = params.HTML
	}
	if params.Archived != nil {
		payload["archived"] = *params.Archived
	}
	raw, err := h.p.client.send(ctx, "PUT", "/api/comments/"+strconv.Itoa(params.ID), payload)
	h.p.noted(err)
	if err != nil {
		return plugins.ApplyResult{}, wrapIndeterminate(err)
	}
	return applied(raw)
}

func (h *commentUpdate) Observe(ctx context.Context, params CommentUpdateParams) (commentState, error) {
	return h.p.readComment(ctx, params.ID)
}

// CommentDeleteParams names a comment to remove.
type CommentDeleteParams struct {
	ID int `json:"id" jsonschema:"the comment's numeric id"`
}

type commentDelete struct{ p *Plugin }

func (h *commentDelete) Plan(ctx context.Context, params CommentDeleteParams) (plugins.Plan[commentState], error) {
	var plan plugins.Plan[commentState]
	if err := h.p.mutationReady(); err != nil {
		return plan, err
	}
	before, err := h.p.readComment(ctx, params.ID)
	if err != nil {
		return plan, err
	}
	return plugins.Plan[commentState]{
		Before:        before,
		Desired:       commentState{Exists: false, ID: params.ID},
		Preconditions: map[string]any{"exists": true, "updated_at": before.Updated},
		Changes: []operations.Change{
			{Field: "comment", From: forDiff(before.HTML), To: nil},
			{Field: "recoverable", From: false, To: false},
		},
		Impact: "Destroys the comment. There is no recycle bin for comments — if " +
			"the aim is only to get it out of the way, archive it instead.",
	}, nil
}

func (h *commentDelete) Apply(ctx context.Context, params CommentDeleteParams, _ plugins.Plan[commentState]) (plugins.ApplyResult, error) {
	_, err := h.p.client.send(ctx, "DELETE", "/api/comments/"+strconv.Itoa(params.ID), nil)
	h.p.noted(err)
	if err != nil {
		return plugins.ApplyResult{}, wrapIndeterminate(err)
	}
	return plugins.ApplyResult{UpstreamRef: strconv.Itoa(params.ID)}, nil
}

func (h *commentDelete) Observe(ctx context.Context, params CommentDeleteParams) (commentState, error) {
	got, err := h.p.readComment(ctx, params.ID)
	if isNotFound(err) {
		return commentState{Exists: false, ID: params.ID}, nil
	}
	return got, err
}

// readComment reads one comment into the state these mutations compare.
func (p *Plugin) readComment(ctx context.Context, id int) (commentState, error) {
	if id <= 0 {
		return commentState{}, fmt.Errorf("bookstack: a comment id is required; " +
			"list_comments reports them")
	}
	raw, err := p.client.get(ctx, "/api/comments/"+strconv.Itoa(id), nil)
	p.noted(err)
	if err != nil {
		if isNotFound(err) {
			return commentState{Exists: false, ID: id}, err
		}
		return commentState{}, err
	}
	var d commentRow
	if err := json.Unmarshal(raw, &d); err != nil {
		return commentState{}, fmt.Errorf("bookstack: could not read the comment: %w", err)
	}
	return commentState{
		Exists: true, ID: d.ID, PageID: d.CommentableID, HTML: d.HTML,
		Archived: d.Archived, Updated: d.UpdatedAt,
	}, nil
}

// commentsOn lists a page's comments.
func (p *Plugin) commentsOn(ctx context.Context, pageID int) ([]commentRow, error) {
	res, err := p.listComments(ctx, onPageArgs{PageID: pageID})
	if err != nil {
		return nil, err
	}
	return res.Comments, nil
}

// --- attachments ------------------------------------------------------------

// attachmentState is what the attachment mutations observe.
type attachmentState struct {
	Exists     bool   `json:"exists"`
	ID         int    `json:"id,omitempty"`
	Name       string `json:"name,omitempty"`
	UploadedTo int    `json:"uploaded_to,omitempty"`
	External   bool   `json:"external,omitempty"`
	Link       string `json:"link,omitempty"`
	Updated    string `json:"updated_at,omitempty"`
}

// AttachmentCreateParams attaches a link to a page.
type AttachmentCreateParams struct {
	PageID int    `json:"page_id" jsonschema:"the page to attach it to"`
	Name   string `json:"name" jsonschema:"what to call the attachment"`
	Link   string `json:"link" jsonschema:"the URL to attach"`
}

type attachmentCreate struct{ p *Plugin }

func (h *attachmentCreate) Plan(ctx context.Context, params AttachmentCreateParams) (plugins.Plan[attachmentState], error) {
	var plan plugins.Plan[attachmentState]
	if err := h.p.mutationReady(); err != nil {
		return plan, err
	}
	name := strings.TrimSpace(params.Name)
	link := strings.TrimSpace(params.Link)
	if params.PageID <= 0 || name == "" || link == "" {
		return plan, fmt.Errorf("bookstack: an attachment needs a page, a name and a link")
	}
	pageState, err := h.p.readEntity(ctx, "pages", params.PageID)
	if err != nil {
		return plan, err
	}
	if !pageState.Exists {
		return plan, fmt.Errorf("bookstack: there is no page %d to attach to", params.PageID)
	}
	return plugins.Plan[attachmentState]{
		Before: attachmentState{Exists: false, UploadedTo: params.PageID},
		Desired: attachmentState{
			Exists: true, Name: name, UploadedTo: params.PageID,
			External: true, Link: link,
		},
		Preconditions: map[string]any{"exists": false, "page_id": params.PageID},
		Changes: []operations.Change{
			{Field: "page", From: nil, To: pageState.Name},
			{Field: "attachment", From: nil, To: name},
			{Field: "link", From: nil, To: link},
		},
		Impact: fmt.Sprintf("Attaches a link called %q to %q. Anybody who can see "+
			"the page can follow it.", name, pageState.Name),
	}, nil
}

func (h *attachmentCreate) Apply(ctx context.Context, params AttachmentCreateParams, _ plugins.Plan[attachmentState]) (plugins.ApplyResult, error) {
	raw, err := h.p.client.send(ctx, "POST", "/api/attachments", map[string]any{
		"name": strings.TrimSpace(params.Name), "link": strings.TrimSpace(params.Link),
		"uploaded_to": params.PageID,
	})
	h.p.noted(err)
	if err != nil {
		return plugins.ApplyResult{}, wrapIndeterminate(err)
	}
	return applied(raw)
}

func (h *attachmentCreate) Observe(ctx context.Context, params AttachmentCreateParams) (attachmentState, error) {
	res, err := h.p.listAttachments(ctx, onPageArgs{PageID: params.PageID})
	if err != nil {
		return attachmentState{}, err
	}
	for _, a := range res.Attachments {
		if strings.EqualFold(a.Name, strings.TrimSpace(params.Name)) {
			return attachmentState{
				Exists: true, ID: a.ID, Name: a.Name,
				UploadedTo: a.UploadedTo, External: a.External, Updated: a.UpdatedAt,
			}, nil
		}
	}
	return attachmentState{Exists: false, UploadedTo: params.PageID}, nil
}

// AttachmentUpdateParams changes an attachment.
type AttachmentUpdateParams struct {
	ID     int    `json:"id" jsonschema:"the attachment's numeric id"`
	Name   string `json:"name,omitempty" jsonschema:"a new name"`
	Link   string `json:"link,omitempty" jsonschema:"a new URL, for a link attachment"`
	PageID int    `json:"page_id,omitempty" jsonschema:"move it to this page"`
}

type attachmentUpdate struct{ p *Plugin }

func (h *attachmentUpdate) Plan(ctx context.Context, params AttachmentUpdateParams) (plugins.Plan[attachmentState], error) {
	var plan plugins.Plan[attachmentState]
	if err := h.p.mutationReady(); err != nil {
		return plan, err
	}
	before, err := h.p.readAttachment(ctx, params.ID)
	if err != nil {
		return plan, err
	}
	desired := before
	changes := []operations.Change{}
	if n := strings.TrimSpace(params.Name); n != "" && n != before.Name {
		desired.Name = n
		changes = diffField(changes, "name", before.Name, n)
	}
	if l := strings.TrimSpace(params.Link); l != "" && l != before.Link {
		if !before.External {
			// Sending a link for an uploaded file replaces the file with a
			// link, and the file is gone. Worth refusing rather than diffing.
			return plan, fmt.Errorf("bookstack: attachment %d is an uploaded file, "+
				"not a link. Replacing it with a link would destroy the file, so "+
				"this refuses; delete it and attach the link separately if that "+
				"is what you mean", params.ID)
		}
		desired.Link = l
		changes = diffField(changes, "link", before.Link, l)
	}
	if params.PageID > 0 && params.PageID != before.UploadedTo {
		desired.UploadedTo = params.PageID
		changes = diffField(changes, "page", before.UploadedTo, params.PageID)
	}
	if len(changes) == 0 {
		return plan, fmt.Errorf("bookstack: nothing to change on attachment %d", params.ID)
	}
	return plugins.Plan[attachmentState]{
		Before:        before,
		Desired:       desired,
		Preconditions: map[string]any{"exists": true, "updated_at": before.Updated},
		Changes:       changes,
		Impact:        fmt.Sprintf("Changes the attachment %q.", before.Name),
		Rollback: AttachmentUpdateParams{
			ID: params.ID, Name: before.Name, Link: before.Link, PageID: before.UploadedTo,
		},
	}, nil
}

func (h *attachmentUpdate) Apply(ctx context.Context, params AttachmentUpdateParams, _ plugins.Plan[attachmentState]) (plugins.ApplyResult, error) {
	payload := map[string]any{}
	if n := strings.TrimSpace(params.Name); n != "" {
		payload["name"] = n
	}
	if l := strings.TrimSpace(params.Link); l != "" {
		payload["link"] = l
	}
	if params.PageID > 0 {
		payload["uploaded_to"] = params.PageID
	}
	raw, err := h.p.client.send(ctx, "PUT", "/api/attachments/"+strconv.Itoa(params.ID), payload)
	h.p.noted(err)
	if err != nil {
		return plugins.ApplyResult{}, wrapIndeterminate(err)
	}
	return applied(raw)
}

func (h *attachmentUpdate) Observe(ctx context.Context, params AttachmentUpdateParams) (attachmentState, error) {
	return h.p.readAttachment(ctx, params.ID)
}

// AttachmentDeleteParams names an attachment to remove.
type AttachmentDeleteParams struct {
	ID int `json:"id" jsonschema:"the attachment's numeric id"`
}

type attachmentDelete struct{ p *Plugin }

func (h *attachmentDelete) Plan(ctx context.Context, params AttachmentDeleteParams) (plugins.Plan[attachmentState], error) {
	var plan plugins.Plan[attachmentState]
	if err := h.p.mutationReady(); err != nil {
		return plan, err
	}
	before, err := h.p.readAttachment(ctx, params.ID)
	if err != nil {
		return plan, err
	}
	impact := fmt.Sprintf("Removes the attachment %q from its page.", before.Name)
	if !before.External {
		impact += " It is an uploaded file, and the file is destroyed with it — " +
			"there is no recycle bin for attachments."
	}
	return plugins.Plan[attachmentState]{
		Before:        before,
		Desired:       attachmentState{Exists: false, ID: params.ID},
		Preconditions: map[string]any{"exists": true, "updated_at": before.Updated},
		Changes: []operations.Change{
			{Field: "attachment", From: before.Name, To: nil},
			{Field: "recoverable", From: false, To: false},
		},
		Impact: impact,
	}, nil
}

func (h *attachmentDelete) Apply(ctx context.Context, params AttachmentDeleteParams, _ plugins.Plan[attachmentState]) (plugins.ApplyResult, error) {
	_, err := h.p.client.send(ctx, "DELETE", "/api/attachments/"+strconv.Itoa(params.ID), nil)
	h.p.noted(err)
	if err != nil {
		return plugins.ApplyResult{}, wrapIndeterminate(err)
	}
	return plugins.ApplyResult{UpstreamRef: strconv.Itoa(params.ID)}, nil
}

func (h *attachmentDelete) Observe(ctx context.Context, params AttachmentDeleteParams) (attachmentState, error) {
	got, err := h.p.readAttachment(ctx, params.ID)
	if isNotFound(err) {
		return attachmentState{Exists: false, ID: params.ID}, nil
	}
	return got, err
}

// readAttachment reads one attachment into the state these mutations compare.
func (p *Plugin) readAttachment(ctx context.Context, id int) (attachmentState, error) {
	if id <= 0 {
		return attachmentState{}, fmt.Errorf("bookstack: an attachment id is " +
			"required; list_attachments reports them")
	}
	raw, err := p.client.get(ctx, "/api/attachments/"+strconv.Itoa(id), nil)
	p.noted(err)
	if err != nil {
		if isNotFound(err) {
			return attachmentState{Exists: false, ID: id}, err
		}
		return attachmentState{}, err
	}
	var d struct {
		attachmentRow
		Links struct {
			HTML string `json:"html"`
		} `json:"links"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return attachmentState{}, fmt.Errorf("bookstack: could not read the attachment: %w", err)
	}
	s := attachmentState{
		Exists: true, ID: d.ID, Name: d.Name, UploadedTo: d.UploadedTo,
		External: d.External, Updated: d.UpdatedAt,
	}
	if d.External {
		// For a link attachment BookStack returns the target as the content.
		s.Link = d.Content
	}
	return s, nil
}
