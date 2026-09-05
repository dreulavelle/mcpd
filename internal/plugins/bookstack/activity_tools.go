package bookstack

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/plugins"
)

// The tools for "what has been happening, and what was deleted".
//
// These are the reads somebody reaches for after something has gone wrong: a
// page that is not where it was, a change nobody owns up to, an attachment
// that vanished. The recycle bin in particular is worth reading before
// concluding anything is lost -- deleting content in BookStack moves it there
// rather than destroying it.

func (p *Plugin) registerActivityTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_recycle_bin",
		Title: "List the recycle bin",
		Description: "What has been deleted and can still be restored, with who " +
			"deleted it and when. Deleting a shelf, book, chapter or page puts " +
			"it here rather than destroying it.",
		Idempotent: true,
		Capability: auth.CapAdmin,
	}, p.listRecycleBin)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_audit_events",
		Title: "Read the audit log",
		Description: "What has been done in the knowledge base and by whom. " +
			"Narrow by event type, by user, or to one item.",
		Idempotent: true,
		Capability: auth.CapAdmin,
	}, p.listAuditEvents)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_comments",
		Title: "List comments",
		Description: "Comments left on pages, optionally only those on one page. " +
			"Includes archived ones, which are marked.",
		Idempotent: true,
	}, p.listComments)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_comment",
		Title: "Get one comment",
		Description: "One comment with its text and who wrote it. The listing " +
			"does not carry the text; this does.",
		Idempotent: true,
	}, p.getComment)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_attachments",
		Title: "List attachments",
		Description: "Files and links attached to pages, optionally only those " +
			"on one page. An external attachment is a link rather than a file.",
		Idempotent: true,
	}, p.listAttachments)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_images",
		Title: "List images",
		Description: "Images in the gallery, both those used in page content " +
			"and those used as covers.",
		Idempotent: true,
	}, p.listImages)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_system",
		Title: "About this instance",
		Description: "The BookStack version, the instance's name and its base " +
			"address. Cheap, and reads none of the content.",
		Idempotent: true,
	}, p.getSystem)
}

// --- recycle bin ------------------------------------------------------------

type recycleRow struct {
	ID        int    `json:"id"`
	DeletedBy int    `json:"deleted_by"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	// Deletable is the thing that was deleted. Its shape depends on what it
	// was, so only the fields every kind carries are named.
	Deletable struct {
		ID        int    `json:"id"`
		Name      string `json:"name"`
		Slug      string `json:"slug"`
		BookID    int    `json:"book_id"`
		ChapterID int    `json:"chapter_id"`
		UpdatedAt string `json:"updated_at"`
	} `json:"deletable"`
	DeletableType string `json:"deletable_type"`
}

// RecycleRow is one deleted thing.
type RecycleRow struct {
	// DeletionID is what restore_from_recycle_bin takes. It is not the item's
	// own id, and confusing the two restores the wrong thing.
	DeletionID int    `json:"deletion_id"`
	Type       string `json:"type,omitempty"`
	ItemID     int    `json:"item_id,omitempty"`
	Name       string `json:"name,omitempty"`
	BookID     int    `json:"book_id,omitempty"`
	ChapterID  int    `json:"chapter_id,omitempty"`
	DeletedBy  int    `json:"deleted_by,omitempty"`
	DeletedAt  string `json:"deleted_at,omitempty"`
}

// RecycleBinResult is the recycle bin listing.
type RecycleBinResult struct {
	Items []RecycleRow `json:"items"`
	Count int          `json:"count"`
	truncation
}

func (p *Plugin) listRecycleBin(ctx context.Context, args limitArgs) (RecycleBinResult, error) {
	if err := p.ready(); err != nil {
		return RecycleBinResult{}, err
	}
	pg, err := p.client.list(ctx, "/api/recycle-bin", nil, args.Limit)
	p.noted(err)
	if err != nil {
		return RecycleBinResult{}, explainPeopleFailure(err, "settings")
	}
	raw, err := decodeRows[recycleRow](pg)
	if err != nil {
		return RecycleBinResult{}, err
	}
	rows := make([]RecycleRow, 0, len(raw))
	for _, d := range raw {
		rows = append(rows, RecycleRow{
			DeletionID: d.ID, Type: shortType(d.DeletableType),
			ItemID: d.Deletable.ID, Name: d.Deletable.Name,
			BookID: d.Deletable.BookID, ChapterID: d.Deletable.ChapterID,
			DeletedBy: d.DeletedBy, DeletedAt: d.CreatedAt,
		})
	}
	rows, cut := bound(rows, pg)
	return RecycleBinResult{Items: rows, Count: len(rows), truncation: cut}, nil
}

// shortType turns BookStack's PHP class name into the word people use.
func shortType(in string) string {
	switch in {
	case "BookStack\\Entities\\Models\\Page", "page":
		return "page"
	case "BookStack\\Entities\\Models\\Chapter", "chapter":
		return "chapter"
	case "BookStack\\Entities\\Models\\Book", "book":
		return "book"
	case "BookStack\\Entities\\Models\\Bookshelf", "bookshelf":
		return "shelf"
	}
	return in
}

// --- audit log --------------------------------------------------------------

type auditArgs struct {
	Type   string `json:"type,omitempty" jsonschema:"only events of this type, such as page_update or book_delete"`
	UserID int    `json:"user_id,omitempty" jsonschema:"only events by this user"`
	Limit  int    `json:"limit,omitempty" jsonschema:"most events to return; the instance's ceiling applies"`
}

type auditRow struct {
	ID           int    `json:"id"`
	Type         string `json:"type"`
	Detail       string `json:"detail"`
	UserID       int    `json:"user_id"`
	LoggableID   int    `json:"loggable_id"`
	LoggableType string `json:"loggable_type"`
	IP           string `json:"ip"`
	CreatedAt    string `json:"created_at"`
	User         *struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"user"`
}

// AuditRow is one thing somebody did.
type AuditRow struct {
	ID       int    `json:"id"`
	Type     string `json:"type"`
	Detail   string `json:"detail,omitempty"`
	UserID   int    `json:"user_id,omitempty"`
	UserName string `json:"user_name,omitempty"`
	ItemID   int    `json:"item_id,omitempty"`
	ItemType string `json:"item_type,omitempty"`
	IP       string `json:"ip,omitempty"`
	At       string `json:"at,omitempty"`
}

// AuditResult is the audit listing.
type AuditResult struct {
	Events []AuditRow `json:"events"`
	Count  int        `json:"count"`
	truncation
}

func (p *Plugin) listAuditEvents(ctx context.Context, args auditArgs) (AuditResult, error) {
	if err := p.ready(); err != nil {
		return AuditResult{}, err
	}
	q := url.Values{}
	if args.Type != "" {
		q.Set("filter[type]", args.Type)
	}
	if args.UserID > 0 {
		q.Set("filter[user_id]", strconv.Itoa(args.UserID))
	}
	pg, err := p.client.list(ctx, "/api/audit-log", q, args.Limit)
	p.noted(err)
	if err != nil {
		return AuditResult{}, explainPeopleFailure(err, "settings")
	}
	raw, err := decodeRows[auditRow](pg)
	if err != nil {
		return AuditResult{}, err
	}
	rows := make([]AuditRow, 0, len(raw))
	for _, a := range raw {
		row := AuditRow{
			ID: a.ID, Type: a.Type, Detail: a.Detail, UserID: a.UserID,
			ItemID: a.LoggableID, ItemType: shortType(a.LoggableType),
			IP: a.IP, At: a.CreatedAt,
		}
		if a.User != nil {
			row.UserName = a.User.Name
		}
		rows = append(rows, row)
	}
	rows, cut := bound(rows, pg)
	return AuditResult{Events: rows, Count: len(rows), truncation: cut}, nil
}

// --- comments, attachments, images ------------------------------------------

type onPageArgs struct {
	PageID int `json:"page_id,omitempty" jsonschema:"only items on this page"`
	Limit  int `json:"limit,omitempty" jsonschema:"most rows to return; the instance's ceiling applies"`
}

// commentRow is one comment.
//
// BookStack's comments are polymorphic: the page is commentable_id with
// commentable_type "page", not page_id. That matters beyond naming --
// filter[page_id] is accepted, silently ignored, and answers with every
// comment in the instance, so a listing built on the old field name is wrong
// rather than empty.
//
// created_by is an id in a listing and an object in a single read, so it is
// left raw here and read by whoever needs it.
type commentRow struct {
	ID              int    `json:"id"`
	CommentableID   int    `json:"commentable_id"`
	CommentableType string `json:"commentable_type"`
	ParentID        *int   `json:"parent_id"`
	// LocalID is the number shown beside the comment on the page, and what a
	// reply refers to.
	LocalID   int    `json:"local_id"`
	HTML      string `json:"html,omitempty"`
	Archived  bool   `json:"archived"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// CommentsResult is the comment listing.
type CommentsResult struct {
	// Notes say what the listing cannot tell you.
	Notes    []string     `json:"notes,omitempty"`
	Comments []commentRow `json:"comments"`
	Count    int          `json:"count"`
	truncation
}

func (p *Plugin) listComments(ctx context.Context, args onPageArgs) (CommentsResult, error) {
	if err := p.ready(); err != nil {
		return CommentsResult{}, err
	}
	q := url.Values{}
	if args.PageID > 0 {
		q.Set("filter[commentable_id]", strconv.Itoa(args.PageID))
		q.Set("filter[commentable_type]", "page")
	}
	pg, err := p.client.list(ctx, "/api/comments", q, args.Limit)
	p.noted(err)
	if err != nil {
		return CommentsResult{}, err
	}
	rows, err := decodeRows[commentRow](pg)
	if err != nil {
		return CommentsResult{}, err
	}
	rows, cut := bound(rows, pg)
	out := CommentsResult{Comments: rows, Count: len(rows), truncation: cut}
	if len(rows) > 0 {
		// The listing genuinely does not carry the text -- BookStack sends it
		// only on a single read -- so a model must be told rather than left to
		// conclude the comments are empty.
		out.Notes = append(out.Notes,
			"These rows carry no comment text. Call get_comment with an id to read one.")
	}
	out.Notes = append(out.Notes, narrowing(cut)...)
	return out, nil
}

type attachmentRow struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Extension  string `json:"extension"`
	UploadedTo int    `json:"uploaded_to"`
	// External marks a link rather than a stored file.
	External  bool   `json:"external"`
	Order     int    `json:"order"`
	CreatedBy int    `json:"created_by"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// AttachmentsResult is the attachment listing.
type AttachmentsResult struct {
	Attachments []attachmentRow `json:"attachments"`
	Count       int             `json:"count"`
	truncation
}

func (p *Plugin) listAttachments(ctx context.Context, args onPageArgs) (AttachmentsResult, error) {
	if err := p.ready(); err != nil {
		return AttachmentsResult{}, err
	}
	q := url.Values{}
	if args.PageID > 0 {
		q.Set("filter[uploaded_to]", strconv.Itoa(args.PageID))
	}
	pg, err := p.client.list(ctx, "/api/attachments", q, args.Limit)
	p.noted(err)
	if err != nil {
		return AttachmentsResult{}, err
	}
	rows, err := decodeRows[attachmentRow](pg)
	if err != nil {
		return AttachmentsResult{}, err
	}
	rows, cut := bound(rows, pg)
	return AttachmentsResult{Attachments: rows, Count: len(rows), truncation: cut}, nil
}

type imageRow struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	URL        string `json:"url"`
	Path       string `json:"path"`
	Type       string `json:"type"`
	UploadedTo int    `json:"uploaded_to"`
	CreatedBy  int    `json:"created_by"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

// ImagesResult is the image listing.
type ImagesResult struct {
	Images []imageRow `json:"images"`
	Count  int        `json:"count"`
	truncation
}

func (p *Plugin) listImages(ctx context.Context, args limitArgs) (ImagesResult, error) {
	if err := p.ready(); err != nil {
		return ImagesResult{}, err
	}
	pg, err := p.client.list(ctx, "/api/image-gallery", nil, args.Limit)
	p.noted(err)
	if err != nil {
		return ImagesResult{}, err
	}
	rows, err := decodeRows[imageRow](pg)
	if err != nil {
		return ImagesResult{}, err
	}
	rows, cut := bound(rows, pg)
	return ImagesResult{Images: rows, Count: len(rows), truncation: cut}, nil
}

// --- the instance -----------------------------------------------------------

type systemArgs struct{}

func (p *Plugin) getSystem(ctx context.Context, _ systemArgs) (SystemInfo, error) {
	if err := p.ready(); err != nil {
		return SystemInfo{}, err
	}
	info, err := p.client.Probe(ctx)
	p.note(err, info)
	return info, err
}

// CommentDetail is one comment in full.
type CommentDetail struct {
	ID              int      `json:"id"`
	CommentableID   int      `json:"commentable_id"`
	CommentableType string   `json:"commentable_type,omitempty"`
	LocalID         int      `json:"local_id,omitempty"`
	ParentID        int      `json:"parent_id,omitempty"`
	HTML            string   `json:"html"`
	Archived        bool     `json:"archived,omitempty"`
	CreatedBy       *userRef `json:"created_by,omitempty"`
	UpdatedBy       *userRef `json:"updated_by,omitempty"`
	CreatedAt       string   `json:"created_at,omitempty"`
	UpdatedAt       string   `json:"updated_at,omitempty"`
}

func (p *Plugin) getComment(ctx context.Context, args idArgs) (CommentDetail, error) {
	if err := p.ready(); err != nil {
		return CommentDetail{}, err
	}
	if args.ID <= 0 {
		return CommentDetail{}, fmt.Errorf("bookstack: a comment id is required; " +
			"list_comments reports them")
	}
	raw, err := p.client.get(ctx, "/api/comments/"+strconv.Itoa(args.ID), nil)
	p.noted(err)
	if err != nil {
		return CommentDetail{}, describeMissing(err, "comment", args.ID)
	}
	var d struct {
		commentRow
		CreatedBy *userRef `json:"created_by"`
		UpdatedBy *userRef `json:"updated_by"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return CommentDetail{}, fmt.Errorf("bookstack: could not read the comment: %w", err)
	}
	out := CommentDetail{
		ID: d.ID, CommentableID: d.CommentableID, CommentableType: d.CommentableType,
		LocalID: d.LocalID, HTML: d.HTML, Archived: d.Archived,
		CreatedBy: d.CreatedBy, UpdatedBy: d.UpdatedBy,
		CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
	if d.ParentID != nil {
		out.ParentID = *d.ParentID
	}
	return out, nil
}
