package threecx

import (
	"context"
	"net/url"
	"strings"

	"github.com/spoked/mcpd/internal/plugins"
)

// The tools for "why can they not get through".
//
// Two of the most common phone tickets are not about the phone system being
// wrong at all. A handset at somebody's house stops registering because 3CX's
// anti-hacking protection blacklisted the address it calls from, after a
// burst of failed registrations that were probably its own. And a customer
// reports that one of their callers cannot reach them, because that caller's
// number is on the blocked list. Neither is visible from the extension, the
// trunk or the call history: a blocked call leaves no record of having been
// refused, which is exactly why somebody spends an afternoon on it.

func (p *Plugin) registerAccessTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_blocked",
		Title: "List blocked addresses and callers",
		Description: "What the phone system is refusing: network addresses it " +
			"has blacklisted, usually after failed registrations, and caller " +
			"IDs somebody blocked. Reach for this when a handset away from the " +
			"office will not register, or a caller says they cannot get " +
			"through — a blocked call leaves no other trace.",
		Idempotent: true,
	}, p.listBlocked)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_sbcs",
		Title: "List session border controllers",
		Description: "The SBCs and remote handsets the phone system knows, and " +
			"whether each is connected right now. Use it when a whole site is " +
			"unreachable rather than one extension.",
		Idempotent: true,
	}, p.listSBCs)
}

// --- blocked ---------------------------------------------------------------------

type blockedArgs struct {
	Customer string `json:"customer,omitempty" jsonschema:"which customer's phone system, by business name or alias; needed when this instance serves more than one"`
	Query    string `json:"query,omitempty" jsonschema:"only entries whose address, caller ID or description contains this"`
	Limit    int    `json:"limit,omitempty" jsonschema:"most entries to return"`
}

// BlockedAddress is one network address the phone system blocks or allows.
type BlockedAddress struct {
	// Address is a single address or a range, as the phone system holds it.
	Address string `json:"address"`
	// Blocked is false for an allow entry, which is how an address is
	// exempted from the anti-hacking protection.
	Blocked bool   `json:"blocked"`
	AddedBy string `json:"added_by,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Expires string `json:"expires,omitempty"`
}

// BlockedCaller is one caller ID the phone system refuses.
type BlockedCaller struct {
	CallerID string `json:"caller_id"`
	Reason   string `json:"reason,omitempty"`
}

// BlockedResult is what the phone system is refusing.
type BlockedResult struct {
	Customer string `json:"customer"`
	// Addresses blocked, then addresses explicitly allowed: the block list is
	// what somebody is looking for and the allow list is what explains why one
	// address is fine while its neighbour is not.
	Addresses []BlockedAddress `json:"addresses"`
	Callers   []BlockedCaller  `json:"callers"`
	// Counted over everything the phone system holds, not only what came back.
	BlockedAddresses int `json:"blocked_addresses"`
	AllowedAddresses int `json:"allowed_addresses"`
	BlockedCallers   int `json:"blocked_callers"`
	truncation
}

func (p *Plugin) listBlocked(ctx context.Context, args blockedArgs) (BlockedResult, error) {
	acct, err := p.resolve(args.Customer)
	if err != nil {
		return BlockedResult{}, err
	}
	limit := p.limitOf(args.Limit)

	type addrRecord struct {
		IPAddrMask  string `json:"IPAddrMask"`
		BlockType   string `json:"BlockType"`
		AddedBy     string `json:"AddedBy"`
		Description string `json:"Description"`
		ExpiresAt   string `json:"ExpiresAt"`
	}
	addrs, err := list[addrRecord](ctx, acct.client, "Blocklist",
		url.Values{"$select": {"Id,IPAddrMask,BlockType,AddedBy,Description,ExpiresAt"}}, limit)
	if err != nil {
		return BlockedResult{}, acct.call(err)
	}

	out := BlockedResult{
		Customer: acct.name, Addresses: make([]BlockedAddress, 0, len(addrs.Rows)),
		Callers: []BlockedCaller{},
	}
	for _, a := range addrs.Rows {
		blocked := !strings.EqualFold(a.BlockType, "Allow")
		if blocked {
			out.BlockedAddresses++
		} else {
			out.AllowedAddresses++
		}
		if !matches(args.Query, a.IPAddrMask, a.Description, a.AddedBy) {
			continue
		}
		out.Addresses = append(out.Addresses, BlockedAddress{
			Address: a.IPAddrMask, Blocked: blocked, AddedBy: a.AddedBy,
			Reason: a.Description, Expires: a.ExpiresAt,
		})
	}
	// Blocks first: an allow entry is context for them rather than the answer
	// to what is being refused.
	sortStable(out.Addresses, func(x, y BlockedAddress) bool { return x.Blocked && !y.Blocked })

	// The caller-ID list is its own collection and a system that will not
	// serve it should not cost the addresses, which are the half somebody is
	// usually looking for.
	type callerRecord struct {
		CallerID    string `json:"CallerId"`
		Description string `json:"Description"`
	}
	callers, err := list[callerRecord](ctx, acct.client, "BlackListNumbers",
		url.Values{"$select": {"Id,CallerId,Description"}}, limit)
	if err != nil {
		p.deps.Log.DebugContext(ctx, "3cx would not list blocked caller IDs",
			"customer", acct.name, "error", err)
	} else {
		out.BlockedCallers = len(callers.Rows)
		for _, c := range callers.Rows {
			if !matches(args.Query, c.CallerID, c.Description) {
				continue
			}
			out.Callers = append(out.Callers, BlockedCaller{CallerID: c.CallerID, Reason: c.Description})
		}
	}

	out.Addresses, out.truncation = bound(out.Addresses, addrs.Truncated)
	acct.note(nil)
	return out, nil
}

// --- session border controllers ---------------------------------------------------

type sbcArgs struct {
	Customer string `json:"customer,omitempty" jsonschema:"which customer's phone system, by business name or alias; needed when this instance serves more than one"`
}

// SBCRow is one session border controller or remote handset.
type SBCRow struct {
	Name string `json:"name"`
	// Connected is whether the phone system is in touch with it now.
	Connected bool   `json:"connected"`
	Version   string `json:"version,omitempty"`
	Group     string `json:"group,omitempty"`
	// MAC is set when the entry is a single remote handset rather than an SBC
	// serving a site.
	MAC string `json:"mac,omitempty"`
}

// SBCsResult is the remote connectivity picture.
type SBCsResult struct {
	Customer     string   `json:"customer"`
	SBCs         []SBCRow `json:"sbcs"`
	Returned     int      `json:"returned"`
	Disconnected int      `json:"disconnected"`
	truncation
}

func (p *Plugin) listSBCs(ctx context.Context, args sbcArgs) (SBCsResult, error) {
	acct, err := p.resolve(args.Customer)
	if err != nil {
		return SBCsResult{}, err
	}
	// Password and ProvisionLink are on this entity and are never asked for;
	// the transport refuses them by name in any case.
	type record struct {
		Name          string `json:"Name"`
		DisplayName   string `json:"DisplayName"`
		HasConnection bool   `json:"HasConnection"`
		Version       string `json:"Version"`
		Group         string `json:"Group"`
		PhoneMAC      string `json:"PhoneMAC"`
	}
	got, err := list[record](ctx, acct.client, "Sbcs",
		url.Values{"$select": {"Name,DisplayName,HasConnection,Version,Group,PhoneMAC"}}, p.cfg.MaxItems)
	if err != nil {
		return SBCsResult{}, acct.call(err)
	}
	out := SBCsResult{Customer: acct.name, SBCs: make([]SBCRow, 0, len(got.Rows))}
	for _, s := range got.Rows {
		if !s.HasConnection {
			out.Disconnected++
		}
		out.SBCs = append(out.SBCs, SBCRow{
			// The display name is what somebody named it; the other is an
			// identifier the phone system generated.
			Name: firstNonBlank(s.DisplayName, s.Name), Connected: s.HasConnection,
			Version: s.Version, Group: s.Group, MAC: s.PhoneMAC,
		})
	}
	out.SBCs, out.truncation = bound(out.SBCs, got.Truncated)
	out.Returned = len(out.SBCs)
	acct.note(nil)
	return out, nil
}

// sortStable orders in place by a less function, without pulling sort's
// comparator shape into every call site here.
func sortStable[T any](rows []T, less func(a, b T) bool) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && less(rows[j], rows[j-1]); j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}
