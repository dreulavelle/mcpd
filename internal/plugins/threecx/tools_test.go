package threecx

import (
	"context"
	"strings"
	"testing"
)

// Fixtures shaped the way the live v20 API answers, taken from a real system's
// $metadata and responses with the values changed. Every extension record
// deliberately carries the credential fields the real API returns by default,
// so a test can prove they never reach a result -- the fake refuses a
// select-less read, but a fixture that never had the fields could not catch a
// tool that asked for them.
const (
	sipPassword   = "SIPSECRET-do-not-leak"
	vmPIN         = "PIN-9182"
	deskPassword  = "DESK-web-pass"
	provisionLink = "https://pbx.example/provisioning/abcdef/cfg.xml"
	licenseKey    = "ABCD-EFGH-IJKL-MNOP"
)

var credentialWords = []string{sipPassword, vmPIN, deskPassword, provisionLink, licenseKey}

func userRecord(number, name string, registered bool, group int, extra string) string {
	return `{"Id":` + number + `,"Number":"` + number + `","DisplayName":"` + name + `","FirstName":"` + name + `","LastName":"",` +
		`"EmailAddress":"` + strings.ToLower(name) + `@acme.example","Enabled":true,"IsRegistered":` + boolText(registered) + `,` +
		`"CurrentProfileName":"Available","QueueStatus":"LoggedIn","PrimaryGroupId":` + itoa(group) + `,` +
		`"AuthID":"` + number + `","AuthPassword":"` + sipPassword + `","VMPIN":"` + vmPIN + `","DeskphonePassword":"` + deskPassword + `","SIPID":"sip-` + number + `"` + extra + `}`
}

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func itoa(i int) string { return strings.TrimSpace(strings.Repeat(" ", 0) + intString(i)) }

func intString(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

const groupsFixture = `{"value":[
 {"Id":28,"Name":"DEFAULT","Number":"","IsDefault":true,"CurrentGroupHours":"ForceClosed","TimeZoneId":"14","OverrideExpiresAt":"2026-09-04T08:00:00Z",
  "Hours":{"Type":"OfficeHours","IgnoreHolidays":false,"Periods":[
    {"DayOfWeek":"Monday","Start":"09:00:00","Stop":"17:30:00"},
    {"DayOfWeek":"Tuesday","Start":"09:00:00","Stop":"17:30:00"}]}},
 {"Id":191,"Name":"Sales","Number":"","IsDefault":false,"CurrentGroupHours":"Default","Hours":{"Type":"AllHours","Periods":[]}}
]}`

const trunksFixture = `{"value":[
 {"Id":30,"Number":"10000","ExternalNumber":"15550100","Direction":"Both","IsOnline":true,"DidNumbers":["15550100","15550101","15550102"],
  "SimultaneousCalls":8,"EnableInboundCalls":true,"EnableOutboundCalls":true,"OutboundCallerID":"15550100",
  "AuthID":"trunkuser","AuthPassword":"` + sipPassword + `","Gateway":{"Name":"Acme SIP","Host":"sip.provider.example","Type":"Provider"}},
 {"Id":31,"Number":"10001","Direction":"Both","IsOnline":false,"DidNumbers":["15550102"],
  "SimultaneousCalls":2,"EnableInboundCalls":true,"EnableOutboundCalls":false,"Gateway":{"Name":"Backup","Host":"sip2.provider.example","Type":"Provider"}}
]}`

const inboundRulesFixture = `{"value":[
 {"Id":1,"RuleName":"Main","Condition":"BasedOnDID","Data":"15550100","CallType":"AllCalls",
  "AlterDestinationDuringOutOfOfficeHours":true,"AlterDestinationDuringHolidays":false,
  "OfficeHoursDestination":{"To":"Queue","Number":"800","Name":"Sales Queue","External":""},
  "OutOfOfficeHoursDestination":{"To":"VoiceMail","Number":"100","Name":"Alice","External":""},
  "HolidaysDestination":{"To":"None","Number":"","Name":"","External":""},
  "TrunkDN":{"Id":30,"Number":"10000","Name":"Acme SIP"}},
 {"Id":2,"RuleName":"Fax","Condition":"BasedOnDID","Data":"15550101","CallType":"AllCalls",
  "OfficeHoursDestination":{"To":"Extension","Number":"101","Name":"Bob","External":""},
  "TrunkDN":{"Id":30,"Number":"10000","Name":"Acme SIP"}}
]}`

func acmeFixtures() map[string]string {
	return map[string]string{
		"SystemStatus": `{"FQDN":"pbx.example","Version":"20.0.9.995","Activated":true,"OS":"Linux","MaxSimCalls":8,"CallsActive":1,
			"ExtensionsTotal":40,"ExtensionsRegistered":3,"TrunksTotal":2,"TrunksRegistered":1,"HasNotRunningServices":true,
			"HasUnregisteredSystemExtensions":false,"DiskUsage":91,"FreeDiskSpace":5500000000,"BackupScheduled":false,
			"AutoUpdateEnabled":true,"LicenseActive":true,"ExpirationDate":"2027-01-01T00:00:00Z","LicenseKey":"` + licenseKey + `"}`,
		"LicenseStatus": `{"ProductCode":"3CXPSPROFSPLA","CompanyName":"Acme","LicenseKey":"` + licenseKey + `"}`,
		"Services": collection(3,
			`{"Name":"3CXPhoneSystem","DisplayName":"3CX PhoneSystem","Status":"Running","MemoryUsed":104857600}`,
			`{"Name":"3CXMediaServer","DisplayName":"3CX Media Server","Status":"Stopped","MemoryUsed":0}`,
			`{"Name":"3CXQueueManager","DisplayName":"","Status":"Running","MemoryUsed":2097152}`),
		"Trunks":       trunksFixture,
		"InboundRules": inboundRulesFixture,
		"Groups":       groupsFixture,
		"Users": collection(3,
			userRecord("100", "Alice", true, 28, ""),
			userRecord("101", "Bob", false, 191, ""),
			userRecord("102", "Carol", false, 28, `,"Enabled":false`)),
	}
}

// The status report says in words what the numbers say, names the trunk that
// is offline and the service that is stopped, and carries no licence key.
func TestGetSystemStatus_DrawsTheFindingsOut(t *testing.T) {
	p, _ := toolPlugin(t, acmeFixtures())
	s, err := p.getSystemStatus(context.Background(), statusArgs{})
	if err != nil {
		t.Fatal(err)
	}
	mustNotContain(t, s, credentialWords...)

	if s.Product != "3CXPSPROFSPLA" || s.Company != "Acme" {
		t.Errorf("licence detail missing: %+v", s)
	}
	if len(s.TrunksOffline) != 1 || s.TrunksOffline[0] != "10001 (Backup)" {
		t.Errorf("the offline trunk should be named with its provider, got %v", s.TrunksOffline)
	}
	if len(s.ServicesNotRunning) != 1 || s.ServicesNotRunning[0] != "3CX Media Server" {
		t.Errorf("the stopped service should be named, got %v", s.ServicesNotRunning)
	}
	if s.FreeDiskGB != 5.5 {
		t.Errorf("free disk %v GB, want 5.5", s.FreeDiskGB)
	}
	joined := strings.Join(s.Concerns, " | ")
	for _, want := range []string{"only 3 of 40 extensions", "1 of 2 trunks are offline: 10001 (Backup)", "3CX Media Server", "91% full", "no backup"} {
		if !strings.Contains(joined, want) {
			t.Errorf("concerns should say %q, got %v", want, s.Concerns)
		}
	}
}

// A status where nothing is wrong has no concerns, rather than a list of
// things that are fine.
func TestConcerns_QuietWhenHealthy(t *testing.T) {
	got := concerns(SystemStatus{
		ExtensionsTotal: 40, ExtensionsRegistered: 38, TrunksTotal: 2, TrunksRegistered: 2,
		LicenseActive: true, Activated: true, BackupScheduled: true, DiskUsedPercent: 40,
	})
	if len(got) != 0 {
		t.Errorf("a healthy system has no concerns, got %v", got)
	}
	got = concerns(SystemStatus{ExtensionsTotal: 40, ExtensionsRegistered: 0, TrunksTotal: 2, TrunksRegistered: 0, LicenseActive: true, Activated: true, BackupScheduled: true})
	if len(got) < 2 || !strings.Contains(got[0], "no extensions are registered") || !strings.Contains(got[1], "no trunks are registered") {
		t.Errorf("nothing registered is the loudest finding and comes first, got %v", got)
	}
}

// The extension list carries names, registration and department, and none of
// the credentials the same records hold.
func TestListExtensions_NeverCarriesCredentials(t *testing.T) {
	p, f := toolPlugin(t, acmeFixtures())
	res, err := p.listExtensions(context.Background(), extensionsArgs{})
	if err != nil {
		t.Fatal(err)
	}
	mustNotContain(t, res, credentialWords...)
	if res.Customer != "Acme" {
		t.Errorf("the answer should name the customer, got %q", res.Customer)
	}
	if res.Total != 3 || res.Returned != 3 {
		t.Errorf("total %d returned %d; want 3 and 3", res.Total, res.Returned)
	}
	if res.Unregistered != 2 || res.Disabled != 1 {
		t.Errorf("unregistered %d disabled %d; want 2 and 1", res.Unregistered, res.Disabled)
	}
	if res.Truncated {
		t.Errorf("a complete listing is not truncated, got %+v", res.truncation)
	}
	byNumber := map[string]ExtensionRow{}
	for _, e := range res.Extensions {
		byNumber[e.Number] = e
	}
	if byNumber["101"].Department != "Sales" || byNumber["100"].Department != "DEFAULT" {
		t.Errorf("departments should be resolved by primary group: %+v", res.Extensions)
	}

	// The request that went upstream named its fields and none of them is a
	// credential.
	for _, seen := range f.seen {
		if strings.Contains(seen, "Users") && !strings.Contains(seen, "%24select=") {
			t.Errorf("a read of Users without $select: %s", seen)
		}
		lower := strings.ToLower(seen)
		for _, bad := range []string{"authpassword", "vmpin", "sipid", "deskphonepassword"} {
			if strings.Contains(lower, bad) {
				t.Errorf("a request asked for %s: %s", bad, seen)
			}
		}
	}
}

// Filters are pushed to the PBX as OData rather than applied here, and a
// department is resolved to its id first.
func TestListExtensions_FiltersUpstream(t *testing.T) {
	p, f := toolPlugin(t, acmeFixtures())
	_, err := p.listExtensions(context.Background(), extensionsArgs{Query: "o'brien", OnlyUnregistered: true, Department: "sales"})
	if err != nil {
		t.Fatal(err)
	}
	var users string
	for _, seen := range f.seen {
		if strings.Contains(seen, "/Users?") {
			users = seen
		}
	}
	for _, want := range []string{"contains%28DisplayName%2C%27o%27%27brien%27%29", "IsRegistered+eq+false", "PrimaryGroupId+eq+191"} {
		if !strings.Contains(users, want) {
			t.Errorf("the Users request should carry %s, got %s", want, users)
		}
	}
	_, err = p.listExtensions(context.Background(), extensionsArgs{Department: "Marketing"})
	if err == nil || !strings.Contains(err.Error(), "DEFAULT, Sales") {
		t.Errorf("an unknown department should be refused naming the real ones, got %v", err)
	}
}

// One extension in full: the forwarding profiles are read as phrases, the key
// layout is parsed out of its XML, the department and role come off the
// membership, and the reasons calls may not land are worked out.
func TestGetExtension_ExplainsItself(t *testing.T) {
	fx := acmeFixtures()
	fx["Users"] = collection(1, userRecord("101", "Bob", false, 191, `,
		"CurrentProfileName":"Out of office","QueueStatus":"LoggedOut","AllowLanOnly":true,"PbxDeliversAudio":true,
		"Blfs":"<PhoneDevice><BLFS><BLF ID=\"29\" BLFNo=\"2\" BLFType=\"BLF\" BLFTypeID=\"0\">100</BLF><BLF ID=\"LOGGEDINQUEUE\" BLFNo=\"3\" BLFType=\"QueueLogin\" BLFTypeID=\"4\">DEFINED BY ID</BLF><BLF ID=\"-1\" BLFNo=\"1\" BLFType=\"CustomSpeedDial\" BLFTypeID=\"2\">15550199\nFront desk</BLF></BLFS></PhoneDevice>",
		"Phones":[{"Id":5,"Name":"Yealink T54W","TemplateName":"yealinkT54W.ph.xml","MacAddress":"805EC0AABBCC","Interface":"pbx.example","ProvisioningLinkExt":"`+provisionLink+`"}],
		"ForwardingProfiles":[
		  {"Id":1,"Name":"Available","CustomName":"","NoAnswerTimeout":20,"AcceptMultipleCalls":true,"RingMyMobile":false,
		   "AvailableRoute":{"NoAnswerInternal":{"To":"VoiceMail","Number":"101","Name":"Bob"},"NoAnswerExternal":{"To":"Extension","Number":"100","Name":"Alice"},
		     "BusyInternal":{"To":"None"},"BusyExternal":{"To":"External","External":"+15551234567"},
		     "NotRegisteredInternal":{"To":"VoiceMail","Number":"101"},"NotRegisteredExternal":{"To":"VoiceMail","Number":"101"}},"AwayRoute":null},
		  {"Id":2,"Name":"Out of office","CustomName":"Holiday","NoAnswerTimeout":20,"AcceptMultipleCalls":false,"RingMyMobile":true,
		   "AvailableRoute":null,"AwayRoute":{"AllHoursInternal":true,"AllHoursExternal":false,"Internal":{"To":"VoiceMail","Number":"101"},"External":{"To":"Queue","Number":"800","Name":"Sales Queue"}}}],
		"Groups":[{"GroupId":191,"Name":"Sales","Number":"","Rights":{"RoleName":"managers"}}]`))
	p, _ := toolPlugin(t, fx)

	e, err := p.getExtension(context.Background(), extensionArgs{Extension: "101"})
	if err != nil {
		t.Fatal(err)
	}
	mustNotContain(t, e, credentialWords...)

	if e.Department != "Sales" || e.Role != "managers" {
		t.Errorf("department and role come off the membership, got %q %q", e.Department, e.Role)
	}
	if len(e.Phones) != 1 || e.Phones[0].MAC != "805EC0AABBCC" || e.Phones[0].Model != "yealinkT54W.ph.xml" {
		t.Errorf("phones: %+v", e.Phones)
	}
	if len(e.Forwarding) != 2 {
		t.Fatalf("forwarding profiles: %+v", e.Forwarding)
	}
	desk := e.Forwarding[0]
	if desk.Kind != "at desk" || desk.RingSeconds != 20 || !desk.AcceptMultipleCalls {
		t.Errorf("desk profile: %+v", desk)
	}
	rules := strings.Join(desk.Rules, " | ")
	for _, want := range []string{"no answer, internal calls: VoiceMail of 101", "no answer, external calls: Extension 100 (Alice)", "busy, internal calls: None", "busy, external calls: External +15551234567"} {
		if !strings.Contains(rules, want) {
			t.Errorf("desk rules should say %q, got %v", want, desk.Rules)
		}
	}
	away := e.Forwarding[1]
	if away.Profile != "Holiday" || away.Kind != "away" || !away.RingMobile {
		t.Errorf("away profile should use its custom name, got %+v", away)
	}
	if strings.Join(away.Rules, " | ") != "internal calls: VoiceMail of 101 (outside office hours too) | external calls: Queue 800 (Sales Queue)" {
		t.Errorf("away rules: %v", away.Rules)
	}

	if len(e.Keys) != 3 || e.Keys[0].No != 1 || e.Keys[0].Kind != "CustomSpeedDial" || e.Keys[0].Target != "15550199" {
		t.Errorf("keys should be sorted by button and a speed dial should show its number only, got %+v", e.Keys)
	}
	if e.Keys[1].Target != "100" || e.Keys[2].Target != "LOGGEDINQUEUE" {
		t.Errorf("a BLF key shows the extension it watches and a queue key its mode, got %+v", e.Keys)
	}

	why := strings.Join(e.WhyCallsMayNotLand, " | ")
	for _, want := range []string{"no handset or app is registered", "Out of office", "logged out of their queues", "lan_only"} {
		if !strings.Contains(why, want) {
			t.Errorf("why_calls_may_not_land should mention %q, got %v", want, e.WhyCallsMayNotLand)
		}
	}
}

// An unknown extension is refused by name and points at the tools that find
// the right one; an empty argument is refused before anything is asked.
func TestGetExtension_Refusals(t *testing.T) {
	fx := acmeFixtures()
	fx["Users"] = collection(0)
	p, f := toolPlugin(t, fx)
	_, err := p.getExtension(context.Background(), extensionArgs{Extension: "999"})
	if err == nil || !strings.Contains(err.Error(), "no extension 999") {
		t.Errorf("want a refusal naming the number, got %v", err)
	}
	reads := f.reads.Load()
	_, err = p.getExtension(context.Background(), extensionArgs{})
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Errorf("an empty extension should be refused, got %v", err)
	}
	if f.reads.Load() != reads {
		t.Error("an empty argument should not reach the PBX")
	}
}

// Trunks carry their numbers, how many of them route somewhere, and a number
// that appears on two trunks is called out on both.
func TestListTrunks_CountsRoutingAndDuplicates(t *testing.T) {
	p, _ := toolPlugin(t, acmeFixtures())
	res, err := p.listTrunks(context.Background(), trunksArgs{})
	if err != nil {
		t.Fatal(err)
	}
	mustNotContain(t, res, credentialWords...)
	if res.Returned != 2 || res.Offline != 1 {
		t.Errorf("returned %d offline %d", res.Returned, res.Offline)
	}
	main := res.Trunks[0]
	if main.Name != "Acme SIP" || main.Host != "sip.provider.example" || main.DIDCount != 3 || main.RoutedDIDs != 2 {
		t.Errorf("main trunk: %+v", main)
	}
	if len(main.DuplicateDIDs) != 1 || main.DuplicateDIDs[0] != "15550102" || len(res.Trunks[1].DuplicateDIDs) != 1 {
		t.Errorf("15550102 is on both trunks and should be flagged on both: %+v / %+v", main.DuplicateDIDs, res.Trunks[1].DuplicateDIDs)
	}
}

// Inbound rules read as where the call goes, with out-of-hours and holiday
// destinations spelled out only where the rule alters them.
func TestListInboundRules_ReadsAsDestinations(t *testing.T) {
	p, _ := toolPlugin(t, acmeFixtures())
	res, err := p.listInboundRules(context.Background(), inboundRulesArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Returned != 2 {
		t.Fatalf("rules: %+v", res.Rules)
	}
	main := res.Rules[0]
	if main.DID != "15550100" || main.Trunk != "10000 (Acme SIP)" || main.OfficeHours != "Queue 800 (Sales Queue)" {
		t.Errorf("main rule: %+v", main)
	}
	if main.OutOfOfficeHours != "VoiceMail of 100" || main.Holidays != "same as office hours" {
		t.Errorf("only the altered destination is spelled out: %+v", main)
	}

	one, err := p.listInboundRules(context.Background(), inboundRulesArgs{Number: "0101"})
	if err != nil || one.Returned != 1 || one.Rules[0].OfficeHours != "Extension 101 (Bob)" {
		t.Errorf("narrowing by digits: %+v %v", one, err)
	}
	none, err := p.listInboundRules(context.Background(), inboundRulesArgs{Trunk: "Backup"})
	if err != nil || none.Returned != 0 {
		t.Errorf("narrowing by trunk name: %+v %v", none, err)
	}
}

// The schedule resolves the default department, reads its hours and closures,
// says when it has been forced, and names the other departments.
func TestGetSchedule_DefaultDepartment(t *testing.T) {
	fx := acmeFixtures()
	fx["Groups(28)"] = `{"Id":28,"OfficeHolidays":[
	  {"Id":1,"Name":"Christmas","Day":25,"Month":12,"Year":0,"DayEnd":25,"MonthEnd":12,"YearEnd":0,"IsRecurrent":true,"TimeOfStartDate":"PT0S","TimeOfEndDate":"PT0S"},
	  {"Id":2,"Name":"Early close","Day":24,"Month":12,"Year":2026,"DayEnd":24,"MonthEnd":12,"YearEnd":2026,"IsRecurrent":false,"TimeOfStartDate":"PT13H30M","TimeOfEndDate":"PT23H59M"},
	  {"Id":3,"Name":"Shutdown","Day":2,"Month":1,"Year":2027,"DayEnd":5,"MonthEnd":1,"YearEnd":2027,"IsRecurrent":false}]}`
	fx["Groups(191)"] = `{"Id":191,"OfficeHolidays":[]}`
	fx["Defs/TimeZones"] = collection(2, `{"Id":"14","Name":"(UTC-05:00) Eastern Time","IanaName":"America/New_York"}`, `{"Id":"1","Name":"UTC","IanaName":"Etc/UTC"}`)
	p, _ := toolPlugin(t, fx)

	s, err := p.getSchedule(context.Background(), scheduleArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Department != "DEFAULT" || !s.IsDefault || s.TimeZone != "America/New_York" {
		t.Errorf("schedule: %+v", s)
	}
	if s.Forced != "forced closed" || s.ForcedUntil != "2026-09-04T08:00:00Z" {
		t.Errorf("the override should be reported with its expiry: %q until %q", s.Forced, s.ForcedUntil)
	}
	if len(s.OfficeHours) != 2 || s.OfficeHours[0].From != "09:00" || s.OfficeHours[0].To != "17:30" {
		t.Errorf("office hours: %+v", s.OfficeHours)
	}
	if len(s.Holidays) != 3 {
		t.Fatalf("holidays: %+v", s.Holidays)
	}
	// Sorted by when they happen within a year: 2 January, 24 December, 25 December.
	if s.Holidays[0].Name != "Shutdown" || s.Holidays[0].Starts != "2027-01-02" || s.Holidays[0].Ends != "2027-01-05" {
		t.Errorf("first holiday: %+v", s.Holidays[0])
	}
	if s.Holidays[1].FromTime != "13:30" || s.Holidays[1].ToTime != "23:59" || s.Holidays[1].Ends != "" {
		t.Errorf("an early closing carries times and no separate end date: %+v", s.Holidays[1])
	}
	if s.Holidays[2].Starts != "--12-25" || !s.Holidays[2].Repeats || s.Holidays[2].FromTime != "" {
		t.Errorf("a repeating whole-day closure: %+v", s.Holidays[2])
	}
	if strings.Join(s.Departments, ",") != "Sales" {
		t.Errorf("the other departments should be named: %v", s.Departments)
	}

	sales, err := p.getSchedule(context.Background(), scheduleArgs{Department: "sales"})
	if err != nil {
		t.Fatal(err)
	}
	if sales.Department != "Sales" || sales.IsDefault || sales.Forced != "" || len(sales.Holidays) != 0 || sales.TimeZone != "" {
		t.Errorf("a named department resolves case-insensitively and reports its own state: %+v", sales)
	}
}

// Named departments resolve case-insensitively; a department that does not
// exist is refused with the ones that do.
func TestGetSchedule_UnknownDepartment(t *testing.T) {
	p, _ := toolPlugin(t, acmeFixtures())
	_, err := p.getSchedule(context.Background(), scheduleArgs{Department: "Support"})
	if err == nil || !strings.Contains(err.Error(), "DEFAULT, Sales") {
		t.Errorf("want a refusal naming the real departments, got %v", err)
	}
}

// Event log lines arrive as templates with the values alongside; the message
// is filled in, and an unfillable placeholder is left rather than blanked.
func TestSearchEvents_FillsTemplates(t *testing.T) {
	fx := acmeFixtures()
	fx["EventLogs"] = collection(2,
		`{"Id":1,"TimeGenerated":"2026-09-03T10:00:00Z","Type":"Warning","Source":"SIP Server","Message":"Trunk %1$s has changed status to %2$s","Params":["10001","unregistered"],"GroupName":"Trunks"}`,
		`{"Id":2,"TimeGenerated":"2026-09-03T09:00:00Z","Type":"Error","Source":"","Message":"Extension %1$s failed: %3$s","Params":["101"]}`)
	p, f := toolPlugin(t, fx)
	res, err := p.searchEvents(context.Background(), eventsArgs{Query: "trunk", Type: "warning", Since: "2026-09-01", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if res.Events[0].Message != "Trunk 10001 has changed status to unregistered" {
		t.Errorf("template not filled: %q", res.Events[0].Message)
	}
	if res.Events[1].Message != "Extension 101 failed: %3$s" {
		t.Errorf("an unfillable placeholder should be left as it was: %q", res.Events[1].Message)
	}
	last := f.seen[len(f.seen)-1]
	for _, want := range []string{"%24search=trunk", "Type+eq+%27Warning%27", "TimeGenerated+ge+2026-09-01T00%3A00%3A00Z"} {
		if !strings.Contains(last, want) {
			t.Errorf("the request should carry %s, got %s", want, last)
		}
	}
	if _, err := p.searchEvents(context.Background(), eventsArgs{Type: "loud"}); err == nil {
		t.Error("an unknown severity should be refused")
	}
	if _, err := p.searchEvents(context.Background(), eventsArgs{Since: "yesterday"}); err == nil || !strings.Contains(err.Error(), "2026-09-01") {
		t.Errorf("a timestamp that will not parse should be refused with the form to use, got %v", err)
	}
}

// Call records read as who rang whom, with the direction worked out, and the
// filters travel upstream as OData with the values escaped.
func TestSearchCallHistory_ReadsAsCalls(t *testing.T) {
	fx := acmeFixtures()
	fx["CallHistoryView"] = collection(2,
		`{"SegmentId":1,"SegmentStartTime":"2026-09-03T10:00:00Z","SegmentEndTime":"2026-09-03T10:02:13Z","CallTime":"PT2M13.5S","CallAnswered":true,
		  "SrcDn":"","SrcDisplayName":"Outside caller","SrcCallerNumber":"15551234567","SrcExternal":true,"DstDn":"100","DstDisplayName":"Alice","DstCallerNumber":"100","DstExternal":false,"SrcRecId":77}`,
		`{"SegmentId":2,"SegmentStartTime":"2026-09-03T09:00:00Z","CallTime":"PT0S","CallAnswered":false,
		  "SrcDn":"101","SrcDisplayName":"Bob","SrcCallerNumber":"101","SrcExternal":false,"DstDn":"","DstCallerNumber":"15559876543","DstExternal":true}`)
	p, f := toolPlugin(t, fx)
	res, err := p.searchCallHistory(context.Background(), callHistoryArgs{Extension: "10'0", Number: "555", Since: "2026-09-01", MissedOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Returned != 2 || res.Answered != 1 || res.Missed != 1 {
		t.Errorf("counts: %+v", res)
	}
	in := res.Calls[0]
	if in.Direction != "inbound" || in.From != "15551234567" || in.To != "100" || in.ToName != "Alice" || in.TalkTime != "2m13s" {
		t.Errorf("inbound call: %+v", in)
	}
	if res.Calls[1].Direction != "outbound" || res.Calls[1].TalkTime != "0s" {
		t.Errorf("outbound call: %+v", res.Calls[1])
	}
	mustNotContain(t, res, "SrcRecId", "77")

	// The PBX was asked at day granularity; the exact bound is applied here, so
	// a window starting at 09:30 drops the 09:00 call the day filter let through.
	narrowed, err := p.searchCallHistory(context.Background(), callHistoryArgs{Since: "2026-09-03T09:30:00Z"})
	if err != nil || narrowed.Returned != 1 || narrowed.Calls[0].At != "2026-09-03T10:00:00Z" {
		t.Errorf("the exact time bound should drop the earlier call: %+v %v", narrowed, err)
	}
	last := f.seen[len(f.seen)-2]
	for _, want := range []string{"SrcDn+eq+%2710%27%270%27", "contains%28SrcCallerNumber%2C%27555%27%29", "date%28SegmentStartTime%29+ge+2026-09-01", "CallAnswered+eq+false"} {
		if !strings.Contains(last, want) {
			t.Errorf("the request should carry %s, got %s", want, last)
		}
	}
}

// Ring groups, queues and receptionists read with their members as phrases and
// their overflow as a destination.
func TestGroupTools_ReadMembers(t *testing.T) {
	fx := acmeFixtures()
	fx["RingGroups"] = collection(1, `{"Id":1,"Number":"801","Name":"Front desk","RingStrategy":"RingAll","RingTime":20,"IsRegistered":true,
		"ForwardNoAnswer":{"To":"VoiceMail","Number":"100"},"Members":[{"Id":29,"Number":"100","Name":"Alice"},{"Id":32,"Number":"101","Name":"Bob"}]}`)
	fx["Queues"] = collection(1, `{"Id":2,"Number":"800","Name":"Sales Queue","PollingStrategy":"LongestWaiting","RingTimeout":15,"MasterTimeout":300,
		"MaxCallersInQueue":10,"SLATime":60,"IsRegistered":true,"ForwardNoAnswer":{"To":"Extension","Number":"100","Name":"Alice"},
		"Agents":[{"Id":29,"Number":"100","Name":"Alice","SkillGroup":"1"}],"Managers":[{"Id":32,"Number":"101","Name":"Bob"}]}`)
	fx["Receptionists"] = collection(1, `{"Id":3,"Number":"900","Name":"Main menu","IVRType":"Default","PromptFilename":"welcome.wav","IsRegistered":true,
		"Timeout":10,"TimeoutForwardType":"Queue","TimeoutForwardDN":"800",
		"Forwards":[{"Id":1,"Input":"1","ForwardType":"Extension","ForwardDN":"100"},{"Id":2,"Input":"0","ForwardType":"RepeatPrompt","ForwardDN":""}]}`)
	p, _ := toolPlugin(t, fx)
	ctx := context.Background()

	rg, err := p.listRingGroups(ctx, ringGroupsArgs{})
	if err != nil || rg.Returned != 1 {
		t.Fatalf("ring groups: %+v %v", rg, err)
	}
	if strings.Join(rg.RingGroups[0].Members, ",") != "100 (Alice),101 (Bob)" || rg.RingGroups[0].NoAnswer != "VoiceMail of 100" {
		t.Errorf("ring group: %+v", rg.RingGroups[0])
	}

	qs, err := p.listQueues(ctx, queuesArgs{})
	if err != nil || qs.Returned != 1 {
		t.Fatalf("queues: %+v %v", qs, err)
	}
	q := qs.Queues[0]
	if q.Agents[0] != "100 (Alice) skill 1" || q.Managers[0] != "101 (Bob)" || q.NoAnswer != "Extension 100 (Alice)" || q.MaxWaitSeconds != 300 {
		t.Errorf("queue: %+v", q)
	}

	rs, err := p.listReceptionists(ctx, receptionistsArgs{})
	if err != nil || rs.Returned != 1 {
		t.Fatalf("receptionists: %+v %v", rs, err)
	}
	r := rs.Receptionists[0]
	if r.OnTimeout != "Queue 800" || strings.Join(r.Menu, ",") != "1: Extension 100,0: RepeatPrompt" {
		t.Errorf("receptionist: %+v", r)
	}
}

// Outbound rules read as a dialling plan: who may use each, and the trunks it
// tries in order with what it does to the digits.
func TestListOutboundRules_ReadsAsAPlan(t *testing.T) {
	fx := acmeFixtures()
	fx["OutboundRules"] = collection(1, `{"Id":1,"Name":"Local","Prefix":"9","Priority":1,"NumberLengthRanges":"10-11","GroupNames":["DEFAULT"],"EmergencyRule":false,
		"DNRanges":[{"From":"100","To":"199"},{"From":"250","To":"250"}],
		"Routes":[{"TrunkId":30,"TrunkName":"Acme SIP","StripDigits":1,"Prepend":"1","Append":"","CallerID":""},{"TrunkId":31,"TrunkName":"Backup","StripDigits":0,"Prepend":"","Append":"","CallerID":"15550100"},{"TrunkId":0,"TrunkName":""}]}`)
	p, _ := toolPlugin(t, fx)
	res, err := p.listOutboundRules(context.Background(), outboundRulesArgs{})
	if err != nil || res.Returned != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	r := res.Rules[0]
	if strings.Join(r.Extensions, ",") != "100-199,250" {
		t.Errorf("extension ranges: %v", r.Extensions)
	}
	if strings.Join(r.Routes, " | ") != "1: Acme SIP (strip 1, prepend 1) | 2: Backup (caller ID 15550100)" {
		t.Errorf("routes: %v", r.Routes)
	}
}

// The directory pushes both the query and the kind to the PBX and refuses a
// kind it has never heard of.
func TestSearchDirectory_FiltersUpstream(t *testing.T) {
	fx := acmeFixtures()
	fx["Peers"] = collection(1, `{"Id":2,"Number":"800","Name":"Sales Queue","Type":"Queue","Hidden":false}`)
	p, f := toolPlugin(t, fx)
	res, err := p.searchDirectory(context.Background(), directoryArgs{Query: "sales", Type: "queue"})
	if err != nil || res.Returned != 1 || res.Entries[0].Type != "Queue" {
		t.Fatalf("%+v %v", res, err)
	}
	last := f.seen[len(f.seen)-1]
	if !strings.Contains(last, "Type+eq+%27Queue%27") || !strings.Contains(last, "contains%28Name%2C%27sales%27%29") {
		t.Errorf("request: %s", last)
	}
	if _, err := p.searchDirectory(context.Background(), directoryArgs{Type: "robot"}); err == nil {
		t.Error("an unknown kind should be refused")
	}
}

// Handsets: the provisioning fields are never asked for, unassigned ones are
// counted, and a query narrows what comes back.
func TestListDevices_NeverAsksForProvisioningLinks(t *testing.T) {
	fx := acmeFixtures()
	fx["DeviceInfos"] = collection(2,
		`{"Id":1,"MAC":"805EC0AABBCC","Vendor":"Yealink","Model":"T54W","FirmwareVersion":"96.86.0.75","NetworkAddress":"10.0.0.21","Assigned":true,"AssignedUser":"100","DetectedAt":"2026-09-03T08:00:00Z","UserAgent":"Yealink SIP-T54W","TemplateName":"yealinkT54W.ph.xml","ViaSBC":false,"InterfaceLink":"`+provisionLink+`"}`,
		`{"Id":2,"MAC":"001565AABBCC","Vendor":"Fanvil","Model":"","FirmwareVersion":"","NetworkAddress":"10.0.0.22","Assigned":false,"AssignedUser":"","UserAgent":"Fanvil X4U","ViaSBC":true,"SbcName":"Branch SBC"}`)
	p, f := toolPlugin(t, fx)
	res, err := p.listDevices(context.Background(), devicesArgs{})
	if err != nil {
		t.Fatal(err)
	}
	mustNotContain(t, res, provisionLink)
	if res.Returned != 2 || res.Unassigned != 1 || res.Devices[1].Model != "Fanvil X4U" || res.Devices[1].ViaSBC != "Branch SBC" {
		t.Errorf("%+v", res)
	}
	last := f.seen[len(f.seen)-1]
	if strings.Contains(strings.ToLower(last), "interfacelink") || strings.Contains(strings.ToLower(last), "parameters") {
		t.Errorf("the request asked for a provisioning field: %s", last)
	}
	narrowed, err := p.listDevices(context.Background(), devicesArgs{Query: "fanvil"})
	if err != nil || narrowed.Returned != 1 {
		t.Errorf("a query should narrow: %+v %v", narrowed, err)
	}
}

// An instance nobody has configured refuses every tool with a message that
// says what to do, and never reaches the network.
func TestTools_RefuseWhenUnconfigured(t *testing.T) {
	p, err := New(testDeps(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.listExtensions(context.Background(), extensionsArgs{})
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Errorf("want a not-configured message, got %v", err)
	}
	if h := p.Check(context.Background()); h.State != "degraded" || !strings.Contains(h.Message, "not configured") {
		t.Errorf("health should say it is unconfigured: %+v", h)
	}
}

// The credential in the plugin's config is blanked, and the descriptor and
// health report carry no address with a password in it.
func TestHelpers_DestinationsAndDurations(t *testing.T) {
	cases := map[string]string{
		isoDuration("PT2M13.5S"): "2m13s",
		isoDuration("PT1H2M3S"):  "1h2m3s",
		isoDuration(""):          "0s",
		clock("PT13H30M"):        "13:30",
		clock("PT0S"):            "",
		clock("PT9H"):            "09:00",
		dateText(0, 12, 25):      "--12-25",
		dateText(2026, 1, 2):     "2026-01-02",
		dateText(0, 0, 0):        "",
		(&destination{To: "Extension", Number: "100", Name: "Alice"}).text(): "Extension 100 (Alice)",
		(&destination{To: "External", External: "+15551234567"}).text():      "External +15551234567",
		(&destination{To: "VoiceMail", Number: "100"}).text():                "VoiceMail of 100",
		(&destination{}).text():    "None",
		(*destination)(nil).text(): "None",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q want %q", got, want)
		}
	}
}
