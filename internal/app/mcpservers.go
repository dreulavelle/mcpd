package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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
		}
		if srv.Parsed != nil {
			view.Title = srv.Parsed.DisplayTitle()
			view.Description = srv.Parsed.Description
			view.Version = srv.Parsed.Version
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
	if err := a.mcpStore.Import(ctx, name, document, doc.Schema, remote.Type, remote.URL); err != nil {
		if errors.Is(err, sqlite.ErrServerExists) {
			return fmt.Errorf("a remote MCP server named %q already exists", name)
		}
		return err
	}
	if err := a.loadMCPServers(ctx); err != nil {
		return err
	}
	a.log.Info("remote MCP server imported",
		"server", name, "document_name", doc.Name, "version", doc.Version,
		"transport", remote.Type, "by", actor)
	return nil
}

// RemoveMCPServer forgets a server, its tool snapshot and its settings.
//
// The settings go with it for the same reason a compiled-in instance's do: a
// name reused later must not silently inherit someone else's credentials.
func (a *App) RemoveMCPServer(ctx context.Context, actor, name string) error {
	srv, ok := a.mcpServer(name)
	if !ok {
		return fmt.Errorf("no remote MCP server named %q", name)
	}

	if err := a.mcpStore.Remove(ctx, name); err != nil {
		if errors.Is(err, sqlite.ErrNoSuchServer) {
			return fmt.Errorf("no remote MCP server named %q", name)
		}
		return err
	}
	if err := a.loadMCPServers(ctx); err != nil {
		return err
	}

	// The instance key is deleted whether or not one was ever written. Nothing
	// in this package writes one for a remote server, and the endpoints that
	// could have are now refused -- but an orphan left by an earlier build is
	// an enabled instance of a type no binary has, which is a host that will
	// not start and a database somebody has to hand-edit. Deleting a key that
	// is not there costs nothing; leaving one that is costs the deployment.
	changes := []settings.Change{{Key: instanceKeyPrefix + name, Delete: true}}

	// The settings go too. Leaving them would mean a name reused later
	// silently inheriting someone else's credentials.
	if srv.Parsed != nil {
		if fields, err := mcpremote.Fields(srv.Parsed); err == nil {
			for _, f := range fields {
				changes = append(changes, settings.Change{
					Key: settings.PluginSettingKey(name, f.Key), Delete: true,
				})
			}
		}
	}
	if err := a.settings.Apply(ctx, actor, changes); err != nil {
		a.log.Warn("removed a remote MCP server but could not clear its settings",
			"server", name, "error", err)
	}

	// Unmount whatever the settings change did not already take down.
	if err := a.reconcileInstance(ctx, name); err != nil {
		a.log.Warn("removed a remote MCP server but could not unmount it",
			"server", name, "error", err)
	}
	a.log.Info("remote MCP server removed", "server", name, "by", actor)
	return nil
}

// SetMCPServerEnabled turns a server on or off.
func (a *App) SetMCPServerEnabled(ctx context.Context, actor, name string, enabled bool) error {
	if _, ok := a.mcpServer(name); !ok {
		return fmt.Errorf("no remote MCP server named %q", name)
	}
	if err := a.mcpStore.SetEnabled(ctx, name, enabled); err != nil {
		return err
	}
	if err := a.loadMCPServers(ctx); err != nil {
		return err
	}
	a.log.Info("remote MCP server toggled", "server", name, "enabled", enabled, "by", actor)
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
		return mcpservers.Diff{}, err
	}
	defer func() {
		if err := probe.Shutdown(context.WithoutCancel(ctx)); err != nil {
			a.log.Warn("discovery client did not close cleanly", "server", name, "error", err)
		}
	}()

	dialCtx, cancel := context.WithTimeout(ctx, discoverTimeout)
	defer cancel()

	seen, err := probe.Discover(dialCtx)
	if err != nil {
		return mcpservers.Diff{}, err
	}

	diff, err := a.mcpStore.Snapshot(ctx, name, seen)
	if err != nil {
		return mcpservers.Diff{}, err
	}
	a.log.Info("remote MCP server discovered",
		"server", name, "by", actor, "offered", len(seen),
		"added", len(diff.Added), "changed", len(diff.Changed), "removed", len(diff.Removed))

	// A discovery can take a tool away, or invalidate an approval by changing
	// the descriptor under it. Either means what is mounted no longer matches
	// what is enabled.
	if err := a.reconcileInstance(ctx, name); err != nil {
		a.log.Warn("could not remount after discovery", "server", name, "error", err)
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

	if err := a.mcpStore.ClassifyTool(ctx, server, tool, hash, state); err != nil {
		if errors.Is(err, sqlite.ErrToolClassification) {
			return fmt.Errorf("%s of %s was not changed: either it has been "+
				"rediscovered with a different description or schema since you "+
				"read it, or it is one this host cannot mount", tool, server)
		}
		return err
	}
	a.log.Info("remote MCP tool classified",
		"server", server, "tool", tool, "state", state, "by", actor)
	return a.reconcileInstance(ctx, server)
}

// mcpFields returns the settings one imported server asks for.
func (a *App) mcpFields(srv mcpservers.Server) ([]settings.Field, error) {
	if srv.Parsed == nil {
		return nil, fmt.Errorf("app: %q was imported in a server.json format this "+
			"build no longer reads", srv.Name)
	}
	return mcpremote.Fields(srv.Parsed)
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
		Document:          srv.Parsed,
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
