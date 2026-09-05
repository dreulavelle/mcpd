package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/mcpservers"
	"github.com/spoked/mcpd/internal/plugins"
	"github.com/spoked/mcpd/internal/plugins/mcpremote"
	"github.com/spoked/mcpd/internal/settings"
	"github.com/spoked/mcpd/internal/storage/sqlite"
)

// mcpInstanceType is what an instance backed by a remote MCP server reports as
// its type.
//
// A compiled-in instance's type names an integration this binary was built
// with. A remote server has no such thing -- what it is, is described by the
// server.json it was imported from -- so the type slot carries the runtime
// instead, and the dashboard reads Runtime rather than parsing this.
const mcpInstanceType = string(plugins.RuntimeMCP)

// discoverTimeout bounds one discovery. Long enough for a cold upstream to
// wake up, short enough that an administrator's request does not hang.
const discoverTimeout = 45 * time.Second

// loadMCPServers refreshes the cached view of the imported servers.
//
// Cached because instances() is consulted on nearly every dashboard request
// and on every settings validation, and reaching the database each time would
// make listing plugins cost a query per call. Invalidated by refreshing after
// every write here, which is the only place they are written.
func (a *App) loadMCPServers(ctx context.Context) error {
	list, err := a.mcpStore.List(ctx)
	if err != nil {
		return err
	}
	byName := make(map[string]mcpservers.Server, len(list))
	for _, srv := range list {
		byName[srv.Name] = srv
	}
	a.mcpMu.Lock()
	a.mcpServers = byName
	a.mcpMu.Unlock()
	return nil
}

// recordDiscovery writes down that a server was asked, and how it went.
//
// Here rather than in the schedule that calls it, so the Discover button and
// the timer record the same fact the same way. Were only the timer to record,
// a manual discovery would leave the timestamp stale and the schedule would
// re-probe a server somebody had just checked by hand.
//
// The cache is refreshed afterwards because loadMCPServers is invalidated by
// refreshing after every write, and this is one -- without it the dashboard
// would keep showing the previous attempt's age until something else wrote.
//
// Failures here are logged and swallowed. The discovery itself either worked
// or did not, and that answer belongs to the caller; failing their request
// because the bookkeeping failed would turn a successful discovery into an
// error an operator cannot act on.
func (a *App) recordDiscovery(ctx context.Context, name string, discoveryErr error) {
	// Its own context and deadline: a discovery cancelled by shutdown must
	// still record that it was attempted, or the next start reads the server
	// as one nothing has ever checked and probes it immediately.
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	if err := a.mcpStore.RecordDiscovery(recordCtx, name, time.Now(), discoveryErr); err != nil {
		a.log.WarnContext(ctx, "could not record a discovery attempt",
			"server", name, "error", err)
		return
	}
	if err := a.loadMCPServers(recordCtx); err != nil {
		a.log.WarnContext(ctx, "could not refresh the server cache after a discovery",
			"server", name, "error", err)
	}
}

// mcpServer returns one imported server from the cache.
func (a *App) mcpServer(name string) (mcpservers.Server, bool) {
	a.mcpMu.RLock()
	defer a.mcpMu.RUnlock()
	srv, ok := a.mcpServers[name]
	return srv, ok
}

// mcpServerNames returns every imported server's name, sorted.
func (a *App) mcpServerNames() []string {
	a.mcpMu.RLock()
	defer a.mcpMu.RUnlock()
	out := make([]string, 0, len(a.mcpServers))
	for name := range a.mcpServers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// MCPServerView is one imported server as the dashboard sees it.
type MCPServerView struct {
	Name          string `json:"name"`
	Title         string `json:"title"`
	Description   string `json:"description"`
	Version       string `json:"version"`
	SchemaVersion string `json:"schema_version"`
	Transport     string `json:"transport"`
	// URL is the template as imported, braces intact. A resolved URL may carry
	// a token that a variable substituted into it, so the template is what is
	// shown.
	URL       string    `json:"url"`
	Enabled   bool      `json:"enabled"`
	Mounted   bool      `json:"mounted"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Readable reports that this build can still parse the stored document.
	// False means the row can be listed and removed and nothing else.
	Readable bool `json:"readable"`
	// Counts summarise the tool snapshot without sending all of it.
	Pending  int `json:"pending"`
	Active   int `json:"enabled_tools"`
	Disabled int `json:"disabled"`
	// ExtraHeaders are the headers an operator added because the published
	// document declared none. Names and whether each is a credential; never a
	// value, which lives encrypted in settings and is not read back.
	ExtraHeaders []MCPHeaderView `json:"extra_headers"`
	// DeclaresNoCredential reports a readable document that names no header
	// and no variable of its own. It is not a claim that the server is open --
	// it is the reason the page offers to add a header, and the difference
	// between an operator who knows to go and find a key and one who reads an
	// empty settings form as "nothing to fill in".
	DeclaresNoCredential bool `json:"declares_no_credential"`

	// Discovery is when this server was last asked what it offers. The tool
	// list on screen is a snapshot, and without this there is no way to tell a
	// snapshot taken an hour ago from one taken in March.
	Discovery mcpservers.Discovery `json:"discovery"`
}

// MCPHeaderView is one operator-added header, without its value.
type MCPHeaderView struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Secret      bool   `json:"secret"`
}

// MCPServers lists every imported server.
func (a *App) MCPServers(ctx context.Context) ([]MCPServerView, error) {
	out := []MCPServerView{}
	for _, name := range a.mcpServerNames() {
		srv, ok := a.mcpServer(name)
		if !ok {
			continue
		}
		view := MCPServerView{
			Name:          srv.Name,
			SchemaVersion: srv.SchemaVersion,
			Transport:     srv.Transport,
			URL:           srv.URL,
			Enabled:       srv.Enabled,
			Mounted:       a.manager.Lookup(srv.Name) != nil,
			CreatedAt:     srv.CreatedAt,
			UpdatedAt:     srv.UpdatedAt,
			Readable:      srv.Parsed != nil,
			Discovery:     srv.Discovery,
		}
		if srv.Parsed != nil {
			view.Title = srv.Parsed.DisplayTitle()
			view.Description = srv.Parsed.Description
			view.Version = srv.Parsed.Version
			if remote, err := srv.Parsed.Remote(); err == nil {
				view.DeclaresNoCredential = len(remote.Headers) == 0 && len(remote.Variables) == 0
			}
		}
		view.ExtraHeaders = []MCPHeaderView{}
		for _, h := range srv.ExtraHeaders {
			view.ExtraHeaders = append(view.ExtraHeaders, MCPHeaderView{
				Name:        h.Name,
				Description: h.Input.Description,
				Secret:      h.Input.IsSecret,
			})
		}
		tools, err := a.mcpStore.Tools(ctx, srv.Name)
		if err != nil {
			return nil, err
		}
		for _, t := range tools {
			switch t.State {
			case mcpservers.ToolPending:
				view.Pending++
			case mcpservers.ToolEnabled:
				view.Active++
			case mcpservers.ToolDisabled:
				view.Disabled++
			}
		}
		out = append(out, view)
	}
	return out, nil
}

// MCPServerTools returns one server's whole tool snapshot, in every state.
func (a *App) MCPServerTools(ctx context.Context, name string) ([]mcpservers.Tool, error) {
	if _, ok := a.mcpServer(name); !ok {
		return nil, fmt.Errorf("no remote MCP server named %q", name)
	}
	return a.mcpStore.Tools(ctx, name)
}

// ImportMCPServer records a new remote server from its server.json.
//
// Importing mounts nothing. The document says how to reach the server; it does
// not say what the server offers, and this host does not take that on trust
// anyway -- an administrator discovers the tools and says which of them may be
// used. Saying so here is the difference between an operator who knows what to
// do next and one who wonders why no tools appeared.
func (a *App) ImportMCPServer(ctx context.Context, actor, name string, document []byte) error {
	if !instanceNamePattern.MatchString(name) {
		return fmt.Errorf("a plugin name must be lowercase letters, digits, "+
			"dashes or underscores, 2 to 32 characters (got %q)", name)
	}
	for _, existing := range a.instances(ctx) {
		if existing.Name == name {
			return fmt.Errorf("a plugin named %q already exists", name)
		}
	}

	// A paste that is a client config is converted rather than refused. It is
	// the file an operator actually has -- their editor's mcpServers block --
	// and telling them it declares no $schema answers a question they did not
	// ask. What comes back is a server.json, judged by exactly the checks
	// below, so nothing is admitted here that a paste could not be.
	if mcpservers.LooksLikeClientConfig(document) {
		converted, err := clientConfigDocument(name, document)
		if err != nil {
			return err
		}
		document = converted
	}

	doc, err := mcpservers.Parse(document)
	if err != nil {
		return err
	}
	remote, err := doc.Remote()
	if err != nil {
		return err
	}
	// Deriving the fields now turns a document this host could store and never
	// render into a refusal at the moment of paste.
	if _, err := mcpremote.Fields(doc); err != nil {
		return err
	}

	// Stored as it was given, not as it was decoded. The document is the
	// operator's, and re-encoding it would quietly drop anything this build
	// does not model.
	if err := a.mcpStore.Import(ctx, actor, name, document, doc.Schema, remote.Type, remote.URL); err != nil {
		if errors.Is(err, sqlite.ErrServerExists) {
			return fmt.Errorf("a remote MCP server named %q already exists", name)
		}
		return err
	}
	if err := a.loadMCPServers(ctx); err != nil {
		return err
	}
	a.log.InfoContext(ctx, "remote MCP server imported",
		"server", name, "document_name", doc.Name, "version", doc.Version,
		"transport", remote.Type, "by", actor)
	return nil
}

// clientConfigDocument picks one server out of a pasted client config.
//
// A config holds several servers and an import records one, so the name the
// operator typed selects which -- the field is already on the form, and asking
// for it twice would be asking the same question in two places. A file holding
// exactly one server needs no such choice.
//
// Every refusal names the entries the file did contain. An operator who pasted
// the wrong file, or one whose servers all run local commands, learns which
// from the message rather than from an empty list afterwards.
func clientConfigDocument(name string, raw []byte) ([]byte, error) {
	entries, err := mcpservers.ParseClientConfig(raw)
	if err != nil {
		return nil, err
	}

	usable := make([]mcpservers.ClientConfigEntry, 0, len(entries))
	for _, e := range entries {
		if e.Document != nil {
			usable = append(usable, e)
		}
	}

	// Exactly one server, and the operator's name is this host's name for it.
	if len(usable) == 1 && (len(entries) == 1 || name == "" || name == usable[0].Name) {
		return usable[0].Document, nil
	}

	for _, e := range usable {
		if e.Name == name {
			return e.Document, nil
		}
	}

	// Named an entry that exists but cannot be imported: say why, once, about
	// the one they asked for.
	for _, e := range entries {
		if e.Name != name || e.Document != nil {
			continue
		}
		msg := fmt.Sprintf("%q in that configuration %s", e.Name, e.Reason)
		if e.Suggestion != "" {
			msg += "; " + e.Suggestion
		}
		return nil, errors.New(msg)
	}

	if len(usable) == 0 {
		return nil, fmt.Errorf("that configuration holds %s, and none of them can be "+
			"reached from here: %s", plural(len(entries), "server"),
			clientConfigSummary(entries))
	}
	return nil, fmt.Errorf("that configuration holds %s. Set the name to the one "+
		"to add: %s", plural(len(usable), "usable server"), clientConfigNames(usable))
}

func clientConfigNames(entries []mcpservers.ClientConfigEntry) string {
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, strconv.Quote(e.Name))
	}
	return strings.Join(names, ", ")
}

// clientConfigSummary says what became of each entry, shortest useful form.
func clientConfigSummary(entries []mcpservers.ClientConfigEntry) string {
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		part := fmt.Sprintf("%q %s", e.Name, e.Reason)
		if e.Suggestion != "" {
			part += " (" + e.Suggestion + ")"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "; ")
}

func plural(n int, noun string) string {
	if n == 1 {
		return "one " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// RemoveMCPServer forgets a server, its tool snapshot and its settings.// RemoveMCPServer forgets a server, its tool snapshot and its settings.
//
// The settings go with it for the same reason a compiled-in instance's do: a
// name reused later must not silently inherit someone else's credentials.
func (a *App) RemoveMCPServer(ctx context.Context, actor, name string) error {
	srv, ok := a.mcpServer(name)
	if !ok {
		return fmt.Errorf("no remote MCP server named %q", name)
	}

	if err := a.mcpStore.Remove(ctx, actor, name); err != nil {
		if errors.Is(err, sqlite.ErrNoSuchServer) {
			return fmt.Errorf("no remote MCP server named %q", name)
		}
		return err
	}
	if err := a.loadMCPServers(ctx); err != nil {
		return err
	}

	// An orphaned instance key is cleared if there is one. Nothing in this
	// package writes one for a remote server, and the endpoints that could
	// have are now refused -- but one left by an earlier build is an enabled
	// instance of a type no binary has, which is a host that will not start
	// and a database somebody has to hand-edit.
	//
	// Read first rather than deleting unconditionally. Every change applied
	// here writes a settings_history row whether or not it matched anything,
	// and operators read that log: recording that an instances. key was
	// removed, on every removal, when normally there was never one, is a note
	// about something that did not happen.
	var changes []settings.Change
	if _, orphaned, err := a.settings.Get(ctx, instanceKeyPrefix+name); err == nil && orphaned {
		a.log.WarnContext(ctx, "clearing an orphaned plugin instance record for a remote MCP server",
			"server", name)
		changes = append(changes, settings.Change{Key: instanceKeyPrefix + name, Delete: true})
	}

	// The settings go too. Leaving them would mean a name reused later
	// silently inheriting someone else's credentials.
	//
	// Only the ones that have a value, for the same reason as above: a field
	// the operator never filled in has nothing to record the removal of, and
	// the history is read by people trying to work out what changed.
	if srv.Parsed != nil {
		// Effective rather than Parsed: an operator-added header has a stored
		// credential too, and leaving it behind is the reuse-of-a-name hazard
		// this sweep exists to prevent.
		if fields, err := mcpremote.Fields(srv.Effective()); err == nil {
			for _, f := range fields {
				key := settings.PluginSettingKey(name, f.Key)
				if _, set, err := a.settings.Get(ctx, key); err != nil || !set {
					continue
				}
				changes = append(changes, settings.Change{Key: key, Delete: true})
			}
		}
	}
	if len(changes) > 0 {
		if err := a.settings.Apply(ctx, actor, changes); err != nil {
			a.log.WarnContext(ctx, "removed a remote MCP server but could not clear its settings",
				"server", name, "error", err)
		}
	}

	// Unmount whatever the settings change did not already take down.
	if err := a.reconcileInstance(ctx, name); err != nil {
		a.log.WarnContext(ctx, "removed a remote MCP server but could not unmount it",
			"server", name, "error", err)
	}
	a.log.InfoContext(ctx, "remote MCP server removed", "server", name, "by", actor)
	return nil
}

// SetMCPServerEnabled turns a server on or off.
func (a *App) SetMCPServerEnabled(ctx context.Context, actor, name string, enabled bool) error {
	if _, ok := a.mcpServer(name); !ok {
		return fmt.Errorf("no remote MCP server named %q", name)
	}
	if err := a.mcpStore.SetEnabled(ctx, actor, name, enabled); err != nil {
		return err
	}
	if err := a.loadMCPServers(ctx); err != nil {
		return err
	}
	a.log.InfoContext(ctx, "remote MCP server toggled", "server", name, "enabled", enabled, "by", actor)
	return a.reconcileInstance(ctx, name)
}

// DiscoverMCPServer asks a server what it offers and records the answer.
//
// It is the only path that calls tools/list, and it is driven by an
// administrator. Discovery does not change what is mounted: a tool arrives
// pending, and something a person has not looked at is not put in front of a
// model.
func (a *App) DiscoverMCPServer(ctx context.Context, actor, name string) (mcpservers.Diff, error) {
	srv, ok := a.mcpServer(name)
	if !ok {
		return mcpservers.Diff{}, fmt.Errorf("no remote MCP server named %q", name)
	}
	if srv.Parsed == nil {
		return mcpservers.Diff{}, fmt.Errorf("%q was imported in a server.json format "+
			"this build no longer reads; remove it and import it again", name)
	}

	// A throwaway client rather than the mounted plugin. A server whose tools
	// are all still pending is not mounted -- which is exactly the state a
	// first discovery runs in -- so discovery cannot depend on one being there.
	probe, err := a.buildMCPPlugin(ctx, srv, nil)
	if err != nil {
		// From here on the attempt has begun, so it is recorded whether or not
		// it works. Everything above this point is a bad argument -- no such
		// server, a document this build cannot read -- and recording those as
		// failed discoveries would put an operator's typo in the column that
		// says whether the far end is answering.
		a.recordDiscovery(ctx, name, err)
		return mcpservers.Diff{}, err
	}
	defer func() {
		if err := probe.Shutdown(context.WithoutCancel(ctx)); err != nil {
			a.log.WarnContext(ctx, "discovery client did not close cleanly", "server", name, "error", err)
		}
	}()

	dialCtx, cancel := context.WithTimeout(ctx, discoverTimeout)
	defer cancel()

	seen, err := probe.Discover(dialCtx)
	if err != nil {
		a.recordDiscovery(ctx, name, err)
		return mcpservers.Diff{}, err
	}

	diff, err := a.mcpStore.Snapshot(ctx, actor, name, seen)
	if err != nil {
		a.recordDiscovery(ctx, name, err)
		return mcpservers.Diff{}, err
	}
	a.recordDiscovery(ctx, name, nil)
	a.log.InfoContext(ctx, "remote MCP server discovered",
		"server", name, "by", actor, "offered", len(seen),
		"added", len(diff.Added), "changed", len(diff.Changed), "removed", len(diff.Removed))

	// A discovery can take a tool away, or invalidate an approval by changing
	// the descriptor under it. Either means what is mounted no longer matches
	// what is enabled.
	if err := a.reconcileInstance(ctx, name); err != nil {
		a.log.WarnContext(ctx, "could not remount after discovery", "server", name, "error", err)
	}
	return diff, nil
}

// ClassifyMCPTool records an administrator's decision about one tool.
//
// The descriptor hash travels with the decision and is part of the guard in
// SQL. What was read and approved was a name, a description and a schema; if a
// discovery replaced them a moment earlier, the decision was about something
// else and is refused rather than applied to the new thing.
func (a *App) ClassifyMCPTool(ctx context.Context, actor, server, tool, hash string, state mcpservers.ToolState) error {
	if _, ok := a.mcpServer(server); !ok {
		return fmt.Errorf("no remote MCP server named %q", server)
	}
	if !state.Valid() {
		return fmt.Errorf("%q is not a tool state; use pending, enabled or disabled", state)
	}
	if hash == "" {
		return errors.New("the tool's descriptor hash is required, so that the " +
			"decision applies to the tool that was read rather than to whatever " +
			"the server offers now")
	}

	if err := a.mcpStore.ClassifyTool(ctx, actor, server, tool, hash, state); err != nil {
		if errors.Is(err, sqlite.ErrToolClassification) {
			// The names are quoted and the colon is gone: "search of graylog
			// was not changed: …" reads as a wrapped Go error to anything
			// deciding whether a message was written for a person, and this
			// one was.
			return fmt.Errorf("%q on %q was not changed. Either it has been "+
				"rediscovered with a different description or schema since you "+
				"read it, or it is one this host cannot mount.", tool, server)
		}
		return err
	}
	a.log.InfoContext(ctx, "remote MCP tool classified",
		"server", server, "tool", tool, "state", state, "by", actor)
	return a.reconcileInstance(ctx, server)
}

// mcpFields returns the settings one imported server asks for.
// AddMCPServerHeader declares a header the published document did not.
//
// This is the operator's answer to a document that says nothing about
// credentials. It records only the declaration; the value is typed on the
// settings page afterwards, into the field this creates, and is encrypted
// there like every other stored credential.
func (a *App) AddMCPServerHeader(ctx context.Context, actor, server, name, description string, secret bool) error {
	srv, ok := a.mcpServer(server)
	if !ok {
		return fmt.Errorf("no remote MCP server named %q", server)
	}
	if srv.Parsed == nil {
		return fmt.Errorf("%q was imported in a server.json format this build "+
			"no longer reads; remove it and import it again", server)
	}
	if err := mcpservers.CheckHeaderName(name); err != nil {
		return err
	}
	// Refused rather than silently ignored. WithHeaders lets the publisher's
	// own declaration win, so accepting this would store a row that changes
	// nothing and leave an operator waiting for a field that never appears.
	if remote, err := srv.Parsed.Remote(); err == nil {
		for _, h := range remote.Headers {
			if strings.EqualFold(h.Name, name) {
				return fmt.Errorf("the document already declares the header %q; "+
					"fill its value in on the settings page", h.Name)
			}
		}
	}

	if err := a.mcpStore.AddHeader(ctx, actor, server, mcpservers.KeyValueInput{
		Name: name,
		Input: mcpservers.Input{
			Description: description,
			IsSecret:    secret,
			IsRequired:  true,
		},
	}); err != nil {
		switch {
		case errors.Is(err, sqlite.ErrNoSuchServer):
			return fmt.Errorf("no remote MCP server named %q", server)
		case errors.Is(err, sqlite.ErrHeaderExists):
			return fmt.Errorf("%q already has a header named %q", server, name)
		}
		return err
	}
	if err := a.loadMCPServers(ctx); err != nil {
		return err
	}
	a.log.InfoContext(ctx, "remote MCP server header declared",
		"server", server, "header", name, "secret", secret, "by", actor)
	// The settings form and the client both come from the document this just
	// changed, so what is mounted no longer matches what is configured.
	if err := a.reconcileInstance(ctx, server); err != nil {
		a.log.WarnContext(ctx, "could not remount after a header was declared",
			"server", server, "error", err)
	}
	return nil
}

// RemoveMCPServerHeader withdraws a header an operator declared.
func (a *App) RemoveMCPServerHeader(ctx context.Context, actor, server, name string) error {
	if _, ok := a.mcpServer(server); !ok {
		return fmt.Errorf("no remote MCP server named %q", server)
	}
	if err := a.mcpStore.RemoveHeader(ctx, actor, server, name); err != nil {
		if errors.Is(err, sqlite.ErrNoSuchHeader) {
			return fmt.Errorf("%q has no header named %q that this host added", server, name)
		}
		return err
	}
	if err := a.loadMCPServers(ctx); err != nil {
		return err
	}
	a.log.InfoContext(ctx, "remote MCP server header withdrawn",
		"server", server, "header", name, "by", actor)
	if err := a.reconcileInstance(ctx, server); err != nil {
		a.log.WarnContext(ctx, "could not remount after a header was withdrawn",
			"server", server, "error", err)
	}
	return nil
}

func (a *App) mcpFields(srv mcpservers.Server) ([]settings.Field, error) {
	if srv.Parsed == nil {
		return nil, fmt.Errorf("app: %q was imported in a server.json format this "+
			"build no longer reads", srv.Name)
	}
	return mcpremote.Fields(srv.Effective())
}

// buildMCPPlugin constructs the runtime for one server.
//
// tools is what Register will mount. Passing nil builds a client that can dial
// and discover and has nothing to serve, which is what discovery needs.
func (a *App) buildMCPPlugin(ctx context.Context, srv mcpservers.Server, tools []mcpservers.Tool) (*mcpremote.Plugin, error) {
	fields, err := a.mcpFields(srv)
	if err != nil {
		return nil, err
	}
	resolved, err := a.resolveFields(ctx, srv.Name, fields)
	if err != nil {
		return nil, err
	}

	values := make(map[string]string, len(resolved))
	for k, v := range resolved {
		if s, ok := v.(string); ok {
			values[k] = s
			continue
		}
		// A number or a boolean still substitutes into a URL as text, which is
		// what the server.json input model says it does.
		if encoded, err := json.Marshal(v); err == nil {
			values[k] = string(trimJSONQuotes(encoded))
		}
	}

	rps := mcpremote.DefaultRequestsPerSecond
	if n, ok := resolved[mcpremote.KeyRequestsPerSecond].(int); ok {
		rps = n
	}

	return mcpremote.New(mcpremote.Options{
		Instance:          srv.Name,
		Document:          srv.Effective(),
		Tools:             tools,
		Values:            values,
		RequestsPerSecond: rps,
		Deps:              a.pluginDeps(srv.Name),
	})
}

// trimJSONQuotes turns a JSON scalar back into the text a URL template wants.
func trimJSONQuotes(b []byte) []byte {
	if len(b) >= 2 && b[0] == '"' && b[len(b)-1] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err == nil {
			return []byte(s)
		}
	}
	return b
}
