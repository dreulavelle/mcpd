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

// The write half.
//
// # Why these are mutations and not tools
//
// A tool call happens. A mutation is planned, shown to somebody in full,
// approved, applied exactly once, and then confirmed by re-reading the target.
// Writing to a knowledge base is the second thing: the failure that matters is
// not an outage, it is a page quietly replaced with something worse and nobody
// noticing for a month.
//
// # What each one claims
//
// Two declarations carry weight and neither is decoration.
//
// Reversible says a rollback can be derived. It is true for the content
// deletes because BookStack sends a deleted shelf, book, chapter or page to
// the recycle bin, from which restore_from_recycle_bin puts it back -- so
// there genuinely is a way back. It is false for everything that destroys:
// emptying the recycle bin, and deleting a comment, an attachment, an image, a
// user or a role, none of which BookStack keeps a copy of. That matters beyond
// documentation: the host refuses to let a standing rule auto-approve an
// irreversible mutation, however broadly the rule is written, because the case
// for authorising a class of change in advance is that a mistake is cheap to
// correct.
//
// Verifiable says Observe genuinely confirms the outcome. Every mutation here
// has a read that answers the same question its write asked, so all of them
// declare it -- and a delete observes absence, which is a real thing to see.
//
// # Drift
//
// Every entity carries updated_at, and a page also carries revision_count.
// Those are the preconditions. A proposal built against a page somebody has
// since edited is refused rather than applied, because the alternative is
// silently discarding their work -- which on a shared knowledge base is the
// failure that actually happens, rather than the dramatic one.

// maxDiffBytes bounds how much of a page's text goes into the diff an approver
// reads.
//
// The whole of it, up to a point: the content *is* the change, and showing a
// summary instead -- "4,182 characters becomes 4,530" -- would be asking
// somebody to approve a thing they have not seen. Past this the diff says it
// was cut, which is at least honest about what was and was not read.
const maxDiffBytes = 8 << 10

// entityState is what a content mutation observes before and after.
//
// One shape for shelves, books, chapters and pages because the fields that
// decide whether a write did what it said are the same for all four. Content
// is only set for a page.
type entityState struct {
	// Exists is what a delete desires to be false and a create desires to be
	// true. Without it, observing a deleted page would be an error rather than
	// the confirmation that it worked.
	Exists    bool   `json:"exists"`
	ID        int    `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Slug      string `json:"slug,omitempty"`
	BookID    int    `json:"book_id,omitempty"`
	ChapterID int    `json:"chapter_id,omitempty"`
	// Description is a shelf's, book's or chapter's blurb.
	Description string `json:"description,omitempty"`
	// Content is a page's text, and the field an approver most needs to read.
	Content string `json:"content,omitempty"`
	// Editor says which form that content is in. Writing markdown to a page
	// BookStack holds as wysiwyg converts it, which is a change in its own
	// right and is diffed as one.
	Editor        string   `json:"editor,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	UpdatedAt     string   `json:"updated_at,omitempty"`
	RevisionCount int      `json:"revision_count,omitempty"`
	// Books are the ids on a shelf, in order.
	Books []int `json:"books,omitempty"`
}

// tagPair is how a caller writes a tag: BookStack stores name and value
// separately, so "customer=Acme" is one tag rather than two.
type tagPair struct {
	Name  string `json:"name" jsonschema:"the tag's name"`
	Value string `json:"value,omitempty" jsonschema:"the tag's value, if it has one"`
}

// apiTags turns a caller's tags into what BookStack takes.
func apiTags(in []tagPair) []map[string]string {
	out := make([]map[string]string, 0, len(in))
	for _, t := range in {
		if strings.TrimSpace(t.Name) == "" {
			continue
		}
		out = append(out, map[string]string{
			"name": strings.TrimSpace(t.Name), "value": strings.TrimSpace(t.Value),
		})
	}
	return out
}

// tagStrings renders stored tags for a diff, as name=value.
func tagStrings(in []tag) []string {
	out := make([]string, 0, len(in))
	for _, t := range in {
		if t.Value == "" {
			out = append(out, t.Name)
			continue
		}
		out = append(out, t.Name+"="+t.Value)
	}
	sortStrings(out)
	return out
}

// wantTagStrings renders a caller's tags the same way, so the two compare.
func wantTagStrings(in []tagPair) []string {
	out := make([]string, 0, len(in))
	for _, t := range in {
		name := strings.TrimSpace(t.Name)
		if name == "" {
			continue
		}
		if v := strings.TrimSpace(t.Value); v != "" {
			out = append(out, name+"="+v)
			continue
		}
		out = append(out, name)
	}
	sortStrings(out)
	return out
}

// diffField adds a change when two values differ.
func diffField(changes []operations.Change, field string, from, to any) []operations.Change {
	if fmt.Sprint(from) == fmt.Sprint(to) {
		return changes
	}
	return append(changes, operations.Change{Field: field, From: from, To: to})
}

// diffText adds a change for a long value, cut to what an approver can read.
func diffText(changes []operations.Change, field, from, to string) []operations.Change {
	if from == to {
		return changes
	}
	return append(changes, operations.Change{
		Field: field, From: forDiff(from), To: forDiff(to),
	})
}

// forDiff cuts a value to the size a diff may carry and says when it did.
func forDiff(s string) string {
	if len(s) <= maxDiffBytes {
		return s
	}
	return strings.ToValidUTF8(s[:maxDiffBytes], "") +
		fmt.Sprintf("\n… (cut here; %d characters in total)", len(s))
}

// preconditionsFor is the snapshot re-checked immediately before a write.
//
// updated_at is what every entity carries, and a page's revision_count moves
// on every save even when the timestamp's second has not. Both, because the
// point is to notice that somebody else got there first.
func preconditionsFor(s entityState) map[string]any {
	pre := map[string]any{"exists": s.Exists}
	if s.UpdatedAt != "" {
		pre["updated_at"] = s.UpdatedAt
	}
	if s.RevisionCount > 0 {
		pre["revision_count"] = s.RevisionCount
	}
	return pre
}

// readEntity reads one shelf, book, chapter or page into the common shape.
//
// A missing entity is not an error here: it is Exists false, which is what a
// delete's Observe needs to see and what a create's Plan reports as the state
// before.
func (p *Plugin) readEntity(ctx context.Context, kind string, id int) (entityState, error) {
	if id <= 0 {
		return entityState{}, nil
	}
	raw, err := p.client.get(ctx, "/api/"+kind+"/"+strconv.Itoa(id), nil)
	p.noted(err)
	if err != nil {
		if isNotFound(err) {
			return entityState{Exists: false, ID: id}, nil
		}
		return entityState{}, err
	}
	var d struct {
		ID            int    `json:"id"`
		Name          string `json:"name"`
		Slug          string `json:"slug"`
		BookID        int    `json:"book_id"`
		ChapterID     int    `json:"chapter_id"`
		Description   string `json:"description"`
		UpdatedAt     string `json:"updated_at"`
		RevisionCount int    `json:"revision_count"`
		Editor        string `json:"editor"`
		HTML          string `json:"html"`
		Markdown      string `json:"markdown"`
		Tags          []tag  `json:"tags"`
		Books         []struct {
			ID int `json:"id"`
		} `json:"books"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return entityState{}, fmt.Errorf("bookstack: could not read the %s: %w",
			singular(kind), err)
	}
	s := entityState{
		Exists: true, ID: d.ID, Name: d.Name, Slug: d.Slug,
		BookID: d.BookID, ChapterID: d.ChapterID, Description: d.Description,
		UpdatedAt: d.UpdatedAt, RevisionCount: d.RevisionCount,
		Editor: d.Editor, Tags: tagStrings(d.Tags),
	}
	if kind == "pages" {
		// The content in the form BookStack holds it. A markdown page's html
		// is generated from its markdown, so the markdown is the source and
		// the thing worth diffing.
		if strings.TrimSpace(d.Markdown) != "" {
			s.Content = d.Markdown
		} else {
			s.Content = d.HTML
		}
	}
	for _, b := range d.Books {
		s.Books = append(s.Books, b.ID)
	}
	return s, nil
}

// mutationEntry is one change this integration can propose, with the
// declaration that describes it.
//
// A table rather than a series of registration calls, so that every change the
// plugin can make is readable in one place -- and so that a test can assert
// what each one claims about itself. Reversible and Risk are not documentation:
// the host reads them to decide whether a standing rule may approve something
// with nobody watching, and a wrong claim there is a change that happens
// unattended.
type mutationEntry struct {
	Spec plugins.MutationSpec
	add  func(r *plugins.Registry, spec plugins.MutationSpec)
}

// entry pairs a declaration with its handler, keeping the handler's types at
// the plugin's own boundary.
func entry[P, S any](spec plugins.MutationSpec, h plugins.MutationHandler[P, S]) mutationEntry {
	return mutationEntry{Spec: spec, add: func(r *plugins.Registry, s plugins.MutationSpec) {
		plugins.Mutation(r, s, h)
	}}
}

// mutations is every change this integration can propose.
func (p *Plugin) mutations() []mutationEntry {
	var all []mutationEntry
	for _, group := range [][]mutationEntry{
		p.pageMutations(),
		p.bookMutations(),
		p.chapterMutations(),
		p.shelfMutations(),
		p.recycleBinMutations(),
		p.commentMutations(),
		p.attachmentMutations(),
		p.peopleMutations(),
		p.permissionMutations(),
	} {
		all = append(all, group...)
	}
	return all
}

// registerMutations declares every change this integration can propose.
func (p *Plugin) registerMutations(r *plugins.Registry) {
	for _, m := range p.mutations() {
		m.add(r, m.Spec)
	}
}

// mutationReady refuses a proposal on an instance nobody has configured.
func (p *Plugin) mutationReady() error {
	if !p.configured {
		return fmt.Errorf("bookstack: not configured yet — add the address and an " +
			"API token ID and secret on the Plugins page before proposing changes")
	}
	return nil
}

// applied is the ordinary success from a write, carrying whatever BookStack
// answered with so the executor has something to reconcile against.
func applied(raw []byte) (plugins.ApplyResult, error) {
	var body struct {
		ID int `json:"id"`
	}
	_ = json.Unmarshal(raw, &body)
	if body.ID > 0 {
		return plugins.ApplyResult{UpstreamRef: strconv.Itoa(body.ID)}, nil
	}
	return plugins.ApplyResult{}, nil
}

// wrapIndeterminate marks a failure where the write may still have landed.
//
// The distinction the executor acts on: an ordinary error means the change did
// not happen and a retry is safe, while an indeterminate one means it may
// have, and retrying would apply it twice. A timeout or a dropped connection
// after the request went out is exactly that case -- BookStack may have
// written the page and failed to tell us.
//
// A refusal BookStack articulated is not indeterminate: a 4xx means it decided
// not to act, and it says so before doing anything.
func wrapIndeterminate(err error) error {
	if err == nil {
		return nil
	}
	if answered(err) {
		return err
	}
	return fmt.Errorf("%w: the request to BookStack did not complete, so the "+
		"change may or may not have been made — check before retrying: %w",
		operations.ErrIndeterminate, err)
}

// answered reports whether BookStack replied at all. A reply, even a refusal,
// settles what happened; silence does not.
func answered(err error) bool {
	msg := err.Error()
	for _, sign := range []string{
		"bookstack refused", "bookstack rejected", "bookstack is throttling",
		"bookstack answered", "bookstack failed to answer",
		ErrNotFound.Error(), ErrForbidden.Error(),
	} {
		if strings.Contains(msg, sign) {
			return true
		}
	}
	return false
}
