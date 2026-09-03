package auth

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Area is one part of this host that a permission is about.
//
// The areas are the dashboard's sections, more or less, because that is what
// an administrator already thinks in when deciding what to hand somebody:
// "they can see approvals but not touch settings" is a sentence about areas.
// A permission is an area at a level, and a role is one level per area.
type Area string

const (
	// AreaApprovals is the queue of proposed changes and deciding on them.
	AreaApprovals Area = "approvals"
	// AreaPolicies is the standing rules and the bypass window.
	AreaPolicies Area = "policies"
	// AreaPlugins is instances, remote servers, the marketplace, and the
	// certificates every outbound connection trusts.
	AreaPlugins Area = "plugins"
	// AreaTunnels is tunnels and the ChatGPT accounts they connect with.
	AreaTunnels Area = "tunnels"
	// AreaSettings is the host's own configuration.
	AreaSettings Area = "settings"
	// AreaAccess is users, groups, roles, keys, registrations and sign-in
	// providers: everything that decides who may do anything else here.
	AreaAccess Area = "access"
	// AreaHistory is the activity ledger, the audit trail, the log stream and
	// performance figures. Write is clearing them.
	AreaHistory Area = "history"
	// AreaSystem is what the host is running and restarting, backing up or
	// restoring it.
	AreaSystem Area = "system"
)

// Areas lists every area in display order.
var Areas = []Area{
	AreaApprovals, AreaPolicies, AreaPlugins, AreaTunnels,
	AreaSettings, AreaAccess, AreaHistory, AreaSystem,
}

// Valid reports whether a is a recognised area.
func (a Area) Valid() bool { return slices.Contains(Areas, a) }

// Levels lists the levels an area can be held at, lowest first. Approvals
// use decide in place of write because approving is not editing, and the
// word on the page should say what the click does.
func (a Area) Levels() []Level {
	if a == AreaApprovals {
		return []Level{LevelRead, LevelDecide}
	}
	return []Level{LevelRead, LevelWrite}
}

// Level is how much of an area, or of a plugin, a subject holds.
//
// Levels are ordered: write includes read, decide includes read. That is the
// whole of the composition rule and it is deliberately the one every product
// people trust with credentials uses -- GitHub's fine-grained tokens, Stripe's
// restricted keys -- because a "write but not read" permission is a thing
// nobody has ever meant.
type Level string

const (
	// LevelNone holds nothing. It is never stored; an area absent from a
	// role is at this level.
	LevelNone Level = "none"
	// LevelRead may look.
	LevelRead Level = "read"
	// LevelWrite may change. Includes read.
	LevelWrite Level = "write"
	// LevelDecide may approve or reject a proposed change. Includes read, and
	// exists only for the approvals area.
	LevelDecide Level = "decide"
)

// rank orders levels so that Includes can compare them.
func (l Level) rank() int {
	switch l {
	case LevelRead:
		return 1
	case LevelWrite, LevelDecide:
		return 2
	}
	return 0
}

// Includes reports whether holding l satisfies a requirement of other.
func (l Level) Includes(other Level) bool {
	if other == LevelNone {
		return true
	}
	return l.rank() >= other.rank() && other.rank() > 0
}

// Valid reports whether l names a level a subject can hold. None is not one:
// it is the absence of a permission rather than a permission.
func (l Level) Valid() bool {
	return l == LevelRead || l == LevelWrite || l == LevelDecide
}

// Permission is one area at one level, written "area:level".
//
// A string, so that the session can carry a list of them, a route table can
// name one per route, and the dashboard can ask "can I settings:write" with
// the same words the server refuses with.
type Permission string

// Perm builds a permission.
func Perm(a Area, l Level) Permission { return Permission(string(a) + ":" + string(l)) }

// Every permission a route or a check can name. The list is closed: a
// permission not here cannot be held, so a typo in a route table is a
// compile error rather than a route nobody can reach.
const (
	PermApprovalsRead   Permission = "approvals:read"
	PermApprovalsDecide Permission = "approvals:decide"
	PermPoliciesRead    Permission = "policies:read"
	PermPoliciesWrite   Permission = "policies:write"
	PermPluginsRead     Permission = "plugins:read"
	PermPluginsWrite    Permission = "plugins:write"
	PermTunnelsRead     Permission = "tunnels:read"
	PermTunnelsWrite    Permission = "tunnels:write"
	PermSettingsRead    Permission = "settings:read"
	PermSettingsWrite   Permission = "settings:write"
	PermAccessRead      Permission = "access:read"
	PermAccessWrite     Permission = "access:write"
	PermHistoryRead     Permission = "history:read"
	PermHistoryWrite    Permission = "history:write"
	PermSystemRead      Permission = "system:read"
	PermSystemWrite     Permission = "system:write"

	// PermSignedIn is held by every principal that is not pending. It gates
	// what a person may do to their own account -- name themselves, link a
	// provider -- which is not administering anything and must not depend on
	// a role that could be given nothing at all.
	PermSignedIn Permission = ""
)

// Split returns the area and level a permission names.
func (p Permission) Split() (Area, Level, bool) {
	area, level, ok := strings.Cut(string(p), ":")
	if !ok {
		return "", "", false
	}
	a, l := Area(area), Level(level)
	if !a.Valid() || !l.Valid() || !slices.Contains(a.Levels(), l) {
		return "", "", false
	}
	return a, l, true
}

// Valid reports whether p names a permission that can be held.
func (p Permission) Valid() bool {
	if p == PermSignedIn {
		return true
	}
	_, _, ok := p.Split()
	return ok
}

// Permissions is what a role carries: the highest level held in each area.
//
// A map rather than a list of permissions, because the level in an area is
// one decision -- "settings: write" -- and storing it as two entries that have
// to agree is one more thing to keep in step. Expansion into the permissions
// held, with write standing in for read, happens in Holds and List.
type Permissions map[Area]Level

// Holds reports whether the set satisfies a permission.
func (ps Permissions) Holds(p Permission) bool {
	if p == PermSignedIn {
		return true
	}
	area, level, ok := p.Split()
	if !ok {
		return false
	}
	return ps[area].Includes(level)
}

// List expands the set into every permission it satisfies, in display order.
func (ps Permissions) List() []Permission {
	out := []Permission{}
	for _, a := range Areas {
		held := ps[a]
		for _, l := range a.Levels() {
			if held.Includes(l) {
				out = append(out, Perm(a, l))
			}
		}
	}
	return out
}

// Merge returns the union of two sets: the higher level in every area.
func (ps Permissions) Merge(other Permissions) Permissions {
	out := Permissions{}
	for a, l := range ps {
		out[a] = l
	}
	for a, l := range other {
		if l.rank() > out[a].rank() {
			out[a] = l
		}
	}
	return out
}

// Normalize drops unknown areas, levels an area cannot be held at, and
// entries at none. What is left is what can be stored.
func (ps Permissions) Normalize() Permissions {
	out := Permissions{}
	for a, l := range ps {
		if !a.Valid() || !l.Valid() || !slices.Contains(a.Levels(), l) {
			continue
		}
		out[a] = l
	}
	return out
}

// Validate refuses an entry that cannot be held, naming it. Normalize drops
// such entries silently, which is right for a stored value an older build
// wrote and wrong for a form somebody just filled in.
func (ps Permissions) Validate() error {
	for a, l := range ps {
		if !a.Valid() {
			return fmt.Errorf("auth: %q is not an area", a)
		}
		if l == LevelNone {
			continue
		}
		if !l.Valid() || !slices.Contains(a.Levels(), l) {
			return fmt.Errorf("auth: %s cannot be held at %q", a, l)
		}
	}
	return nil
}

// Equal reports whether two sets hold the same thing.
func (ps Permissions) Equal(other Permissions) bool {
	a, b := ps.Normalize(), other.Normalize()
	if len(a) != len(b) {
		return false
	}
	for area, level := range a {
		if b[area] != level {
			return false
		}
	}
	return true
}

// MarshalJSON writes the set as an object of area to level, areas in display
// order so two equal sets encode identically.
func (ps Permissions) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	b.WriteByte('{')
	first := true
	for _, a := range Areas {
		l, ok := ps[a]
		if !ok || l == LevelNone {
			continue
		}
		if !first {
			b.WriteByte(',')
		}
		first = false
		fmt.Fprintf(&b, "%q:%q", string(a), string(l))
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}

// UnmarshalJSON reads the object form, dropping what this build cannot hold.
func (ps *Permissions) UnmarshalJSON(data []byte) error {
	var raw map[string]string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	out := Permissions{}
	for a, l := range raw {
		out[Area(a)] = Level(l)
	}
	*ps = out.Normalize()
	return nil
}

// Wildcard grants access to every plugin. It is spelled out rather than
// represented by an empty set, so that a misconfigured principal with no
// plugins listed is denied everything rather than granted everything.
const Wildcard = "*"

// Grant is one plugin, or every plugin, at read or write.
//
// Read calls a plugin's read tools. Write also proposes changes through it.
// This is where "read versus read and write" lives, per plugin, so that one
// key can read Graylog and change cnMaestro; a single bit on the role would
// force every credential to be all-read or all-write across everything it
// reaches.
type Grant struct {
	Plugin string `json:"plugin"`
	Level  Level  `json:"level"`
}

// Grants is a subject's reach.
type Grants []Grant

// Reaches reports whether the grants cover plugin at level.
//
// A wildcard covers every plugin at its own level, so a wildcard at read
// beside a named plugin at write means write on that one and read on the rest.
func (gs Grants) Reaches(plugin string, level Level) bool {
	if plugin == "" || plugin == Wildcard {
		return false
	}
	for _, g := range gs {
		if (g.Plugin == Wildcard || g.Plugin == plugin) && g.Level.Includes(level) {
			return true
		}
	}
	return false
}

// LevelFor returns the highest level held for a plugin, or none.
func (gs Grants) LevelFor(plugin string) Level {
	held := LevelNone
	for _, g := range gs {
		if (g.Plugin == Wildcard || g.Plugin == plugin) && g.Level.rank() > held.rank() {
			held = g.Level
		}
	}
	return held
}

// Plugins lists the plugin names the grants reach at any level, with the
// wildcard absorbing everything beside it. For the places that only need to
// know which plugins a caller may see.
func (gs Grants) Plugins() []string {
	names := []string{}
	for _, g := range gs {
		if g.Plugin == Wildcard {
			return []string{Wildcard}
		}
		if !slices.Contains(names, g.Plugin) {
			names = append(names, g.Plugin)
		}
	}
	sort.Strings(names)
	return names
}

// Normalize cleans a grant list before it is stored: blanks dropped, one
// entry per plugin at the highest level named, a named plugin absorbed by a
// wildcard that already covers its level, and a stable order so two equal
// lists encode identically.
//
// A grant at an invalid level is dropped rather than rounded down, because a
// level this build does not recognise is one whose meaning it cannot guess.
func (gs Grants) Normalize() Grants {
	best := map[string]Level{}
	for _, g := range gs {
		name := strings.TrimSpace(g.Plugin)
		if name == "" || (g.Level != LevelRead && g.Level != LevelWrite) {
			continue
		}
		if g.Level.rank() > best[name].rank() {
			best[name] = g.Level
		}
	}
	wild := best[Wildcard]
	out := Grants{}
	for name, level := range best {
		if name != Wildcard && wild.Includes(level) {
			continue
		}
		out = append(out, Grant{Plugin: name, Level: level})
	}
	sort.Slice(out, func(i, j int) bool {
		// The wildcard first, then by name, so a list reads "everything at
		// read, and these at write".
		if (out[i].Plugin == Wildcard) != (out[j].Plugin == Wildcard) {
			return out[i].Plugin == Wildcard
		}
		return out[i].Plugin < out[j].Plugin
	})
	return out
}

// Validate refuses a grant that cannot be stored, naming it.
func (gs Grants) Validate() error {
	for _, g := range gs {
		if strings.TrimSpace(g.Plugin) == "" {
			return fmt.Errorf("auth: a grant names no plugin")
		}
		if g.Level != LevelRead && g.Level != LevelWrite {
			return fmt.Errorf("auth: plugin %q cannot be granted at %q; read or write", g.Plugin, g.Level)
		}
	}
	return nil
}

// UnionGrants folds several grant lists into one, at the highest level
// named for each plugin.
func UnionGrants(lists ...Grants) Grants {
	var all Grants
	for _, l := range lists {
		all = append(all, l...)
	}
	return all.Normalize()
}

// Equal reports whether two grant lists reach the same thing.
func (gs Grants) Equal(other Grants) bool {
	return slices.Equal(gs.Normalize(), other.Normalize())
}

// GrantsAt turns a plain plugin list into grants at one level. For the
// places that still speak in plugin names: a configuration file's `plugins:`
// list, a tunnel narrowed to one system.
func GrantsAt(plugins []string, level Level) Grants {
	out := Grants{}
	for _, p := range plugins {
		out = append(out, Grant{Plugin: p, Level: level})
	}
	return out.Normalize()
}

// DecodeGrants reads a stored grant list. A value this build cannot parse
// reads as no grants, which is the safe direction: a row nobody can decode
// hands out nothing rather than everything.
func DecodeGrants(encoded string) Grants {
	var out Grants
	if err := json.Unmarshal([]byte(encoded), &out); err != nil {
		return Grants{}
	}
	return out.Normalize()
}

// EncodeGrants renders grants for storage.
func EncodeGrants(gs Grants) string {
	encoded, err := json.Marshal(gs.Normalize())
	if err != nil {
		// Cannot happen for a slice of two strings; failing closed rather
		// than asserting.
		return "[]"
	}
	return string(encoded)
}
