package threecx

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/spoked/mcpd/internal/plugins"
)

// The tools for "why is this extension behaving like that".
//
// The complaints these settle are the ones a status page never will: calls
// going straight to voicemail, a phone that never rings, someone missing from
// a queue. Every one of those has a cause sitting on the extension record -- a
// profile left on Do Not Disturb, a forwarding rule somebody set months ago, a
// handset that has not registered -- and none of it is visible from a list.
//
// Every read here names its fields. That is the security boundary rather than
// tidiness: 3CX's user object carries AuthID, AuthPassword, DeskphonePassword,
// VMPIN and SIPID beside these, and the field list is the only thing standing
// between those and whoever asked.

func (p *Plugin) registerExtensionTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_extensions",
		Title: "List extensions",
		Description: "Extensions with name, registration, enabled state, status " +
			"profile and department. Narrow by query, unregistered only, or department.",
		Idempotent: true,
	}, p.listExtensions)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_extension",
		Title: "Get one extension",
		Description: "One extension in full: registration, status, queue login, " +
			"voicemail, remote-access options, department and role, handsets, " +
			"forwarding rules per profile, desk-phone keys, and why calls may not land.",
		Idempotent: true,
	}, p.getExtension)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_devices",
		Title: "List provisioned handsets",
		Description: "Provisioned handsets: make, model, firmware, address, " +
			"assigned extension and when last seen.",
		Idempotent: true,
	}, p.listDevices)
}

// --- listing ----------------------------------------------------------------

type extensionsArgs struct {
	Customer         string `json:"customer,omitempty" jsonschema:"which customer's phone system, by business name or alias; needed when this instance serves more than one"`
	Query            string `json:"query,omitempty" jsonschema:"only extensions whose name, number or email contains this"`
	OnlyUnregistered bool   `json:"only_unregistered,omitempty" jsonschema:"only extensions with no handset or app registered"`
	Department       string `json:"department,omitempty" jsonschema:"only extensions whose primary department has this name or number"`
	Limit            int    `json:"limit,omitempty" jsonschema:"most extensions to return; the instance's ceiling applies"`
}

// ExtensionRow is one extension as a list shows it.
type ExtensionRow struct {
	Number      string `json:"number"`
	Name        string `json:"name"`
	Email       string `json:"email,omitempty"`
	Enabled     bool   `json:"enabled"`
	Registered  bool   `json:"registered"`
	Status      string `json:"status,omitempty"`
	QueueStatus string `json:"queue_status,omitempty"`
	Department  string `json:"department,omitempty"`
}

// ExtensionsResult is the extension list with its counts.
type ExtensionsResult struct {
	// Customer is the business this answer is about, so an answer can never be
	// read as another customer's.
	Customer   string         `json:"customer"`
	Extensions []ExtensionRow `json:"extensions"`
	// Total is how many extensions match on the phone system, which can be
	// more than were returned.
	Total    int `json:"total"`
	Returned int `json:"returned"`
	// Unregistered and Disabled count within the rows returned.
	Unregistered int `json:"unregistered"`
	Disabled     int `json:"disabled"`
	truncation
}

// extensionListFields is the projection for a listing. Nothing here is a
// credential, and adding a name is a decision to send that field to whoever
// asked, including the assistant.
const extensionListFields = "Id,Number,DisplayName,EmailAddress,Enabled,IsRegistered," +
	"CurrentProfileName,QueueStatus,PrimaryGroupId"

type userSummary struct {
	Number         string `json:"Number"`
	DisplayName    string `json:"DisplayName"`
	Email          string `json:"EmailAddress"`
	Enabled        bool   `json:"Enabled"`
	IsRegistered   bool   `json:"IsRegistered"`
	Profile        string `json:"CurrentProfileName"`
	QueueStatus    string `json:"QueueStatus"`
	PrimaryGroupID int    `json:"PrimaryGroupId"`
}

func (p *Plugin) listExtensions(ctx context.Context, args extensionsArgs) (ExtensionsResult, error) {
	acct, err := p.resolve(args.Customer)
	if err != nil {
		return ExtensionsResult{}, err
	}

	// Departments, for the name on each row and for the filter. One small
	// call; a PBX has a handful of groups.
	groups, err := p.readGroups(ctx, acct)
	if err != nil {
		return ExtensionsResult{}, acct.call(err)
	}
	byID := map[int]string{}
	for _, g := range groups {
		byID[g.ID] = g.Name
	}

	var filters []string
	if s := strings.TrimSpace(args.Query); s != "" {
		lit := odataString(s)
		filters = append(filters, fmt.Sprintf(
			"(contains(DisplayName,%s) or contains(Number,%s) or contains(EmailAddress,%s))", lit, lit, lit))
	}
	if args.OnlyUnregistered {
		filters = append(filters, "IsRegistered eq false")
	}
	if d := strings.TrimSpace(args.Department); d != "" {
		g, ok := findGroup(groups, d)
		if !ok {
			return ExtensionsResult{}, fmt.Errorf("there is no department %q; the phone system has %s",
				d, strings.Join(groupNames(groups), ", "))
		}
		filters = append(filters, fmt.Sprintf("PrimaryGroupId eq %d", g.ID))
	}

	q := url.Values{"$select": {extensionListFields}, "$orderby": {"Number"}}
	if len(filters) > 0 {
		q.Set("$filter", strings.Join(filters, " and "))
	}
	got, err := list[userSummary](ctx, acct.client, "Users", q, p.limitOf(args.Limit))
	if err != nil {
		return ExtensionsResult{}, acct.call(err)
	}

	out := ExtensionsResult{Extensions: make([]ExtensionRow, 0, len(got.Rows)), Total: got.Total}
	for _, u := range got.Rows {
		if !u.IsRegistered {
			out.Unregistered++
		}
		if !u.Enabled {
			out.Disabled++
		}
		out.Extensions = append(out.Extensions, ExtensionRow{
			Number: u.Number, Name: u.DisplayName, Email: u.Email,
			Enabled: u.Enabled, Registered: u.IsRegistered, Status: u.Profile,
			QueueStatus: u.QueueStatus, Department: byID[u.PrimaryGroupID],
		})
	}
	if out.Total < 0 {
		out.Total = len(out.Extensions)
	}
	out.Extensions, out.truncation = bound(out.Extensions, got.Truncated)
	out.Returned = len(out.Extensions)
	acct.note(nil)
	out.Customer = acct.name
	return out, nil
}

// --- one extension ------------------------------------------------------------

type extensionArgs struct {
	Customer  string `json:"customer,omitempty" jsonschema:"which customer's phone system, by business name or alias; needed when this instance serves more than one"`
	Extension string `json:"extension" jsonschema:"the extension number"`
}

// Extension is one extension in full.
type Extension struct {
	// Customer is the business this answer is about, so an answer can never be
	// read as another customer's.
	Customer   string `json:"customer"`
	Number     string `json:"number"`
	Name       string `json:"name"`
	FirstName  string `json:"first_name,omitempty"`
	LastName   string `json:"last_name,omitempty"`
	Email      string `json:"email,omitempty"`
	Mobile     string `json:"mobile,omitempty"`
	Enabled    bool   `json:"enabled"`
	Registered bool   `json:"registered"`

	StatusProfile    string `json:"status_profile,omitempty"`
	QueueStatus      string `json:"queue_status,omitempty"`
	OutboundCallerID string `json:"outbound_caller_id,omitempty"`
	Department       string `json:"department,omitempty"`
	Role             string `json:"role,omitempty"`
	Language         string `json:"language,omitempty"`

	Voicemail              bool   `json:"voicemail"`
	VoicemailEmail         string `json:"voicemail_email,omitempty"`
	VoicemailPlaysCallerID bool   `json:"voicemail_plays_caller_id"`
	MissedCallEmails       bool   `json:"missed_call_emails"`
	Hotdesking             bool   `json:"hotdesking"`
	Require2FA             bool   `json:"require_2fa"`
	HiddenInPhonebook      bool   `json:"hidden_in_phonebook"`
	InternalCallsOnly      bool   `json:"internal_calls_only"`
	RecordCalls            bool   `json:"record_calls"`
	CallScreening          bool   `json:"call_screening"`

	// The three settings that decide whether somebody working away from the
	// office can use their phone at all.
	PbxDeliversAudio     bool   `json:"pbx_delivers_audio"`
	BlockRemoteNonTunnel bool   `json:"block_remote_non_tunnel"`
	LanOnly              bool   `json:"lan_only"`
	SRTP                 string `json:"srtp,omitempty"`

	Phones     []PhoneRow      `json:"phones"`
	Forwarding []ForwardingRow `json:"forwarding"`
	Keys       []KeyRow        `json:"keys"`

	// WhyCallsMayNotLand is the reasons this extension might not be ringing,
	// worked out rather than left for a reader to infer from thirty fields.
	WhyCallsMayNotLand []string `json:"why_calls_may_not_land"`
}

// PhoneRow is one handset on an extension.
type PhoneRow struct {
	Name      string `json:"name,omitempty"`
	Model     string `json:"model,omitempty"`
	MAC       string `json:"mac,omitempty"`
	Interface string `json:"interface,omitempty"`
}

// ForwardingRow is one status profile and what it does with calls.
type ForwardingRow struct {
	Profile             string `json:"profile"`
	Kind                string `json:"kind"`
	RingSeconds         int    `json:"ring_seconds,omitempty"`
	AcceptMultipleCalls bool   `json:"accept_multiple_calls"`
	RingMobile          bool   `json:"ring_mobile"`
	// Rules are the profile's routes as phrases: "no answer, internal calls:
	// VoiceMail of 101".
	Rules []string `json:"rules"`
}

// KeyRow is one button on the desk phone.
type KeyRow struct {
	No     int    `json:"no"`
	Kind   string `json:"kind"`
	Target string `json:"target,omitempty"`
}

// extensionFields is the projection for one extension. Same rule as the list.
const extensionFields = "Id,Number,DisplayName,FirstName,LastName,EmailAddress,Mobile," +
	"Enabled,IsRegistered,CurrentProfileName,QueueStatus,OutboundCallerID,Language," +
	"VMEnabled,VMEmailOptions,VMPlayCallerID,SendEmailMissedCalls,EnableHotdesking," +
	"Require2FA,HideInPhonebook,Internal,RecordCalls,CallScreening," +
	"PbxDeliversAudio,BlockTunnel,AllowLanOnly,SRTPMode,Blfs"

// extensionExpand names the fields of each related record too, because an
// expanded property's default projection can carry credentials: Phones without
// a $select returns each handset's provisioning link.
const extensionExpand = "Phones($select=Id,Name,TemplateName,MacAddress,Interface)," +
	"ForwardingProfiles($select=Id,Name,CustomName,NoAnswerTimeout,AcceptMultipleCalls," +
	"RingMyMobile,AvailableRoute,AwayRoute)," +
	"Groups($select=GroupId,Name,Number;$expand=Rights($select=RoleName))"

type userDetail struct {
	Number         string `json:"Number"`
	DisplayName    string `json:"DisplayName"`
	FirstName      string `json:"FirstName"`
	LastName       string `json:"LastName"`
	Email          string `json:"EmailAddress"`
	Mobile         string `json:"Mobile"`
	Enabled        bool   `json:"Enabled"`
	IsRegistered   bool   `json:"IsRegistered"`
	Profile        string `json:"CurrentProfileName"`
	QueueStatus    string `json:"QueueStatus"`
	CallerID       string `json:"OutboundCallerID"`
	Language       string `json:"Language"`
	VMEnabled      bool   `json:"VMEnabled"`
	VMEmailOptions string `json:"VMEmailOptions"`
	VMPlayCallerID bool   `json:"VMPlayCallerID"`
	MissedEmails   bool   `json:"SendEmailMissedCalls"`
	Hotdesking     bool   `json:"EnableHotdesking"`
	Require2FA     bool   `json:"Require2FA"`
	Hidden         bool   `json:"HideInPhonebook"`
	Internal       bool   `json:"Internal"`
	RecordCalls    bool   `json:"RecordCalls"`
	CallScreening  bool   `json:"CallScreening"`
	PbxAudio       bool   `json:"PbxDeliversAudio"`
	BlockTunnel    bool   `json:"BlockTunnel"`
	LanOnly        bool   `json:"AllowLanOnly"`
	SRTPMode       string `json:"SRTPMode"`
	Blfs           string `json:"Blfs"`
	Phones         []struct {
		Name      string `json:"Name"`
		Template  string `json:"TemplateName"`
		MAC       string `json:"MacAddress"`
		Interface string `json:"Interface"`
	} `json:"Phones"`
	Profiles []forwardingProfile `json:"ForwardingProfiles"`
	Groups   []struct {
		Name   string `json:"Name"`
		Rights *struct {
			RoleName string `json:"RoleName"`
		} `json:"Rights"`
	} `json:"Groups"`
}

// forwardingProfile is one status profile as 3CX keeps it. A profile carries
// one of two route objects and the phone system decides which: AvailableRoute
// for a profile that means "at my desk", where what matters is why a call did
// not connect, and AwayRoute for one that means "not here", where every call
// goes the same place.
type forwardingProfile struct {
	Name                string `json:"Name"`
	CustomName          string `json:"CustomName"`
	NoAnswerTimeout     int    `json:"NoAnswerTimeout"`
	AcceptMultipleCalls bool   `json:"AcceptMultipleCalls"`
	RingMyMobile        bool   `json:"RingMyMobile"`
	Available           *struct {
		NoAnswerInternal      *destination `json:"NoAnswerInternal"`
		NoAnswerExternal      *destination `json:"NoAnswerExternal"`
		BusyInternal          *destination `json:"BusyInternal"`
		BusyExternal          *destination `json:"BusyExternal"`
		NotRegisteredInternal *destination `json:"NotRegisteredInternal"`
		NotRegisteredExternal *destination `json:"NotRegisteredExternal"`
	} `json:"AvailableRoute"`
	Away *struct {
		AllHoursInternal bool         `json:"AllHoursInternal"`
		AllHoursExternal bool         `json:"AllHoursExternal"`
		Internal         *destination `json:"Internal"`
		External         *destination `json:"External"`
	} `json:"AwayRoute"`
}

func (p *Plugin) getExtension(ctx context.Context, args extensionArgs) (Extension, error) {
	acct, err := p.resolve(args.Customer)
	if err != nil {
		return Extension{}, err
	}
	number := strings.TrimSpace(args.Extension)
	if number == "" {
		return Extension{}, fmt.Errorf("extension is required: the extension number, as list_extensions reports it")
	}

	q := url.Values{
		"$select": {extensionFields},
		"$expand": {extensionExpand},
		"$filter": {"Number eq " + odataString(number)},
	}
	u, found, err := one[userDetail](ctx, acct.client, "Users", q)
	if err != nil {
		return Extension{}, acct.call(err)
	}
	if !found {
		return Extension{}, fmt.Errorf("there is no extension %s on this phone system; "+
			"list_extensions or search_directory will find the right number", number)
	}

	out := Extension{
		Number: u.Number, Name: firstNonBlank(u.DisplayName, strings.TrimSpace(u.FirstName+" "+u.LastName)),
		FirstName: u.FirstName, LastName: u.LastName, Email: u.Email, Mobile: u.Mobile,
		Enabled: u.Enabled, Registered: u.IsRegistered,
		StatusProfile: u.Profile, QueueStatus: u.QueueStatus, OutboundCallerID: u.CallerID,
		Language:  u.Language,
		Voicemail: u.VMEnabled, VoicemailEmail: u.VMEmailOptions, VoicemailPlaysCallerID: u.VMPlayCallerID,
		MissedCallEmails: u.MissedEmails, Hotdesking: u.Hotdesking, Require2FA: u.Require2FA,
		HiddenInPhonebook: u.Hidden, InternalCallsOnly: u.Internal, RecordCalls: u.RecordCalls,
		CallScreening:    u.CallScreening,
		PbxDeliversAudio: u.PbxAudio, BlockRemoteNonTunnel: u.BlockTunnel, LanOnly: u.LanOnly,
		SRTP:   u.SRTPMode,
		Phones: []PhoneRow{}, Forwarding: []ForwardingRow{}, Keys: []KeyRow{},
		WhyCallsMayNotLand: []string{},
	}
	// The department and role live on the membership joining an extension to
	// a group. The first is the one its page shows.
	if len(u.Groups) > 0 {
		out.Department = u.Groups[0].Name
		if u.Groups[0].Rights != nil {
			out.Role = u.Groups[0].Rights.RoleName
		}
	}
	for _, ph := range u.Phones {
		out.Phones = append(out.Phones, PhoneRow{Name: ph.Name, Model: ph.Template, MAC: ph.MAC, Interface: ph.Interface})
	}
	for _, prof := range u.Profiles {
		out.Forwarding = append(out.Forwarding, forwardingRow(prof))
	}
	out.Keys = parseKeys(u.Blfs)

	if !u.Enabled {
		out.WhyCallsMayNotLand = append(out.WhyCallsMayNotLand, "the extension is disabled, so it cannot be called at all")
	}
	if !u.IsRegistered {
		out.WhyCallsMayNotLand = append(out.WhyCallsMayNotLand, "no handset or app is registered, so there is nothing for a call to ring")
	}
	if pr := strings.ToLower(u.Profile); strings.Contains(pr, "away") || strings.Contains(pr, "dnd") ||
		strings.Contains(pr, "do not disturb") || strings.Contains(pr, "out of office") {
		out.WhyCallsMayNotLand = append(out.WhyCallsMayNotLand,
			fmt.Sprintf("the status profile is %q, which usually diverts calls; see that profile's forwarding rules", u.Profile))
	}
	if strings.EqualFold(u.QueueStatus, "LoggedOut") {
		out.WhyCallsMayNotLand = append(out.WhyCallsMayNotLand, "they are logged out of their queues, so queue calls will skip them")
	}
	if u.LanOnly {
		out.WhyCallsMayNotLand = append(out.WhyCallsMayNotLand, "lan_only is set, so a handset or app outside the office network cannot register")
	}
	acct.note(nil)
	out.Customer = acct.name
	return out, nil
}

// forwardingRow flattens one profile's routes into phrases.
func forwardingRow(prof forwardingProfile) ForwardingRow {
	row := ForwardingRow{
		Profile:             firstNonBlank(prof.CustomName, prof.Name),
		RingSeconds:         prof.NoAnswerTimeout,
		AcceptMultipleCalls: prof.AcceptMultipleCalls,
		RingMobile:          prof.RingMyMobile,
		Rules:               []string{},
	}
	switch {
	case prof.Available != nil:
		row.Kind = "at desk"
		a := prof.Available
		row.Rules = append(row.Rules,
			"no answer, internal calls: "+a.NoAnswerInternal.text(),
			"no answer, external calls: "+a.NoAnswerExternal.text(),
			"busy, internal calls: "+a.BusyInternal.text(),
			"busy, external calls: "+a.BusyExternal.text(),
			"not registered, internal calls: "+a.NotRegisteredInternal.text(),
			"not registered, external calls: "+a.NotRegisteredExternal.text(),
		)
	case prof.Away != nil:
		row.Kind = "away"
		a := prof.Away
		internal, external := "internal calls: "+a.Internal.text(), "external calls: "+a.External.text()
		if a.AllHoursInternal {
			internal += " (outside office hours too)"
		}
		if a.AllHoursExternal {
			external += " (outside office hours too)"
		}
		row.Rules = append(row.Rules, internal, external)
	default:
		row.Kind = "unknown"
	}
	return row
}

// parseKeys reads a desk phone's key layout.
//
// The phone system keeps the whole layout as one string of XML on the
// extension:
//
//	<PhoneDevice><BLFS><BLF ID="31" BLFNo="2" BLFType="BLF" BLFTypeID="0">101</BLF>…</BLFS></PhoneDevice>
//
// A layout this cannot parse is reported as no keys rather than failing the
// whole answer: everything else about the extension is still the answer to
// what was asked, and one odd string should not take it down.
func parseKeys(raw string) []KeyRow {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []KeyRow{}
	}
	var device struct {
		XMLName xml.Name `xml:"PhoneDevice"`
		BLFS    struct {
			Keys []struct {
				No    int    `xml:"BLFNo,attr"`
				Kind  string `xml:"BLFType,attr"`
				ID    string `xml:"ID,attr"`
				Value string `xml:",chardata"`
			} `xml:"BLF"`
		} `xml:"BLFS"`
	}
	if err := xml.Unmarshal([]byte(raw), &device); err != nil {
		return []KeyRow{}
	}
	keys := make([]KeyRow, 0, len(device.BLFS.Keys))
	for _, k := range device.BLFS.Keys {
		target := strings.TrimSpace(k.Value)
		// A custom speed dial holds the number and its labels one per line;
		// the first line is the number, which is the part worth showing.
		if i := strings.IndexAny(target, "\r\n"); i >= 0 {
			target = strings.TrimSpace(target[:i])
		}
		// A queue-login key names its mode in the id and writes a fixed phrase
		// as the value.
		if k.Kind == "QueueLogin" {
			target = k.ID
		}
		keys = append(keys, KeyRow{No: k.No, Kind: k.Kind, Target: target})
	}
	sort.SliceStable(keys, func(a, b int) bool { return keys[a].No < keys[b].No })
	return keys
}

// --- handsets -----------------------------------------------------------------

type devicesArgs struct {
	Customer       string `json:"customer,omitempty" jsonschema:"which customer's phone system, by business name or alias; needed when this instance serves more than one"`
	Query          string `json:"query,omitempty" jsonschema:"only handsets whose MAC, model, vendor, address or assigned extension contains this"`
	UnassignedOnly bool   `json:"unassigned_only,omitempty" jsonschema:"only handsets not yet attached to an extension"`
	Limit          int    `json:"limit,omitempty" jsonschema:"most handsets to return"`
}

// DeviceRow is one provisioned or detected handset.
type DeviceRow struct {
	MAC        string `json:"mac"`
	Vendor     string `json:"vendor,omitempty"`
	Model      string `json:"model,omitempty"`
	Firmware   string `json:"firmware,omitempty"`
	Address    string `json:"address,omitempty"`
	Assigned   bool   `json:"assigned"`
	AssignedTo string `json:"assigned_to,omitempty"`
	LastSeen   string `json:"last_seen,omitempty"`
	Template   string `json:"template,omitempty"`
	ViaSBC     string `json:"via_sbc,omitempty"`
}

// DevicesResult is the handset list.
type DevicesResult struct {
	// Customer is the business this answer is about, so an answer can never be
	// read as another customer's.
	Customer   string      `json:"customer"`
	Devices    []DeviceRow `json:"devices"`
	Returned   int         `json:"returned"`
	Unassigned int         `json:"unassigned"`
	truncation
}

// deviceFields leaves out InterfaceLink, NetworkPath and Parameters: those
// carry provisioning URLs, and a provisioning URL is a credential wearing a
// different hat.
const deviceFields = "Id,MAC,Vendor,Model,FirmwareVersion,NetworkAddress,Assigned,AssignedUser," +
	"DetectedAt,UserAgent,TemplateName,ViaSBC,SbcName"

func (p *Plugin) listDevices(ctx context.Context, args devicesArgs) (DevicesResult, error) {
	acct, err := p.resolve(args.Customer)
	if err != nil {
		return DevicesResult{}, err
	}
	type record struct {
		MAC        string `json:"MAC"`
		Vendor     string `json:"Vendor"`
		Model      string `json:"Model"`
		Firmware   string `json:"FirmwareVersion"`
		Address    string `json:"NetworkAddress"`
		Assigned   bool   `json:"Assigned"`
		User       string `json:"AssignedUser"`
		DetectedAt string `json:"DetectedAt"`
		UserAgent  string `json:"UserAgent"`
		Template   string `json:"TemplateName"`
		ViaSBC     bool   `json:"ViaSBC"`
		SbcName    string `json:"SbcName"`
	}
	q := url.Values{"$select": {deviceFields}}
	if args.UnassignedOnly {
		q.Set("$filter", "Assigned eq false")
	}
	got, err := list[record](ctx, acct.client, "DeviceInfos", q, p.limitOf(args.Limit))
	if err != nil {
		return DevicesResult{}, acct.call(err)
	}
	out := DevicesResult{Devices: make([]DeviceRow, 0, len(got.Rows))}
	for _, d := range got.Rows {
		if !d.Assigned {
			out.Unassigned++
		}
		if !matches(args.Query, d.MAC, d.Model, d.Vendor, d.Address, d.User, d.UserAgent) {
			continue
		}
		row := DeviceRow{
			MAC: d.MAC, Vendor: d.Vendor, Model: firstNonBlank(d.Model, d.UserAgent),
			Firmware: d.Firmware, Address: d.Address, Assigned: d.Assigned,
			AssignedTo: d.User, LastSeen: d.DetectedAt, Template: d.Template,
		}
		if d.ViaSBC {
			row.ViaSBC = firstNonBlank(d.SbcName, "yes")
		}
		out.Devices = append(out.Devices, row)
	}
	out.Devices, out.truncation = bound(out.Devices, got.Truncated)
	out.Returned = len(out.Devices)
	acct.note(nil)
	out.Customer = acct.name
	return out, nil
}
