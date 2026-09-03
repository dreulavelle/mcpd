package threecx

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// recorder is a RoundTripper that records whether a request got through.
type recorder struct{ reached []string }

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.reached = append(r.reached, req.Method+" "+req.URL.Path)
	return &http.Response{StatusCode: 200, Body: http.NoBody, Header: http.Header{}, Request: req}, nil
}

func guarded(t *testing.T) (*http.Client, *recorder) {
	t.Helper()
	rec := &recorder{}
	c := readOnly(&http.Client{Transport: rec}, "https://pbx.example")
	return c, rec
}

func try(t *testing.T, c *http.Client, method, target string) error {
	t.Helper()
	req, err := http.NewRequest(method, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if resp != nil {
		resp.Body.Close()
	}
	return err
}

// A read that names its fields on an allow-listed path reaches the network; a
// read on any other path does not, however it is spelled.
func TestGuard_OnlyAllowListedReadsGetThrough(t *testing.T) {
	c, rec := guarded(t)
	ok := []string{
		"https://pbx.example/xapi/v1/Users?$select=Id,Number",
		"https://pbx.example/xapi/v1/Users(12)?$select=Id,Number",
		"https://pbx.example/xapi/v1/SystemStatus?$select=Version",
		"https://pbx.example/xapi/v1/Defs/TimeZones?$select=Id,Name",
		"https://pbx.example//xapi/v1//Trunks/?$select=Id",
	}
	for _, u := range ok {
		if err := try(t, c, http.MethodGet, u); err != nil {
			t.Errorf("%s should be permitted: %v", u, err)
		}
	}
	if len(rec.reached) != len(ok) {
		t.Errorf("%d requests reached the network, want %d", len(rec.reached), len(ok))
	}

	refused := []string{
		// A credential dump by default projection, whatever it is asked.
		"https://pbx.example/xapi/v1/SipDevices?$select=Id",
		// Not on the list.
		"https://pbx.example/xapi/v1/Recordings?$select=Id",
		"https://pbx.example/xapi/v1/Backups?$select=FileName",
		"https://pbx.example/xapi/v1/MyTokens?$select=Id",
		"https://pbx.example/xapi/v1/$metadata",
		// A path that shares a prefix with an allowed one.
		"https://pbx.example/xapi/v1/UsersExport?$select=Id",
		"https://pbx.example/xapi/v1/Users/Pbx.ExportExtensions?$select=Id",
		// Outside the API root.
		"https://pbx.example/webclient/api/Login/GetAccessToken",
		"https://pbx.example/",
	}
	before := len(rec.reached)
	for _, u := range refused {
		if err := try(t, c, http.MethodGet, u); err == nil {
			t.Errorf("%s should be refused", u)
		}
	}
	if len(rec.reached) != before {
		t.Errorf("a refused request reached the network: %v", rec.reached[before:])
	}
}

// Nothing but the sign-in may be anything other than a GET. This is the
// read-only guarantee, and it holds for paths the integration does read.
func TestGuard_RefusesEveryWrite(t *testing.T) {
	c, rec := guarded(t)
	for _, method := range []string{http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete} {
		err := try(t, c, method, "https://pbx.example/xapi/v1/Users?$select=Id")
		if err == nil || !strings.Contains(err.Error(), "only reads") {
			t.Errorf("%s Users should be refused as a write, got %v", method, err)
		}
	}
	// The sign-in is the one POST, and only as a POST.
	if err := try(t, c, http.MethodPost, "https://pbx.example"+loginPath); err != nil {
		t.Errorf("signing in should be permitted: %v", err)
	}
	if err := try(t, c, http.MethodGet, "https://pbx.example"+loginPath); err == nil {
		t.Error("a GET to the sign-in path is not a sign-in and should be refused")
	}
	if len(rec.reached) != 1 {
		t.Errorf("only the sign-in should have reached the network, got %v", rec.reached)
	}
}

// A read must name its fields. 3CX's default projection of an extension carries
// its SIP password, and the rule holds for every endpoint rather than the ones
// known to leak.
func TestGuard_RequiresSelect(t *testing.T) {
	c, rec := guarded(t)
	for _, u := range []string{
		"https://pbx.example/xapi/v1/Users",
		"https://pbx.example/xapi/v1/Users?$top=1",
		"https://pbx.example/xapi/v1/Groups?$select=",
		"https://pbx.example/xapi/v1/SystemStatus",
	} {
		err := try(t, c, http.MethodGet, u)
		if err == nil || !strings.Contains(err.Error(), "$select") {
			t.Errorf("%s should be refused for naming no fields, got %v", u, err)
		}
	}
	if len(rec.reached) != 0 {
		t.Errorf("a select-less read reached the network: %v", rec.reached)
	}
}

// A credential may not be asked for by name, in a $select, inside an $expand,
// or through a comparison in a $filter.
func TestGuard_RefusesCredentialFields(t *testing.T) {
	c, rec := guarded(t)
	refused := map[string]string{
		"https://pbx.example/xapi/v1/Users?$select=Id,AuthPassword":                                   "AuthPassword",
		"https://pbx.example/xapi/v1/Users?$select=Id,vmpin":                                          "vmpin",
		"https://pbx.example/xapi/v1/Users?$select=Id,SIPID":                                          "SIPID",
		"https://pbx.example/xapi/v1/Users?$select=Id,DeskphonePassword":                              "DeskphonePassword",
		"https://pbx.example/xapi/v1/SystemStatus?$select=Version,LicenseKey":                         "LicenseKey",
		"https://pbx.example/xapi/v1/Trunks?$select=Id,Certificate":                                   "Certificate",
		"https://pbx.example/xapi/v1/DeviceInfos?$select=MAC,InterfaceLink":                           "InterfaceLink",
		"https://pbx.example/xapi/v1/DeviceInfos?$select=MAC,Parameters":                              "Parameters",
		"https://pbx.example/xapi/v1/Users?$select=Id&$expand=Phones($select=Id,ProvisioningLinkExt)": "ProvisioningLinkExt",
		"https://pbx.example/xapi/v1/Users?$select=Id&$expand=Phones($select=Id,Settings)":            "Settings",
		"https://pbx.example/xapi/v1/Users?$select=Id&$filter=startswith(AuthPassword,'a')":           "AuthPassword",
		"https://pbx.example/xapi/v1/Users?$select=Id&$orderby=VMPIN":                                 "VMPIN",
		// A name this package has never heard of, caught by its shape.
		"https://pbx.example/xapi/v1/Users?$select=Id,SomeNewApiSecret": "SomeNewApiSecret",
		"https://pbx.example/xapi/v1/Users?$select=Id,PairingToken":     "PairingToken",
	}
	for u, field := range refused {
		err := try(t, c, http.MethodGet, u)
		if err == nil || !strings.Contains(err.Error(), field) {
			t.Errorf("%s should be refused naming %s, got %v", u, field, err)
		}
	}
	if len(rec.reached) != 0 {
		t.Errorf("a credential read reached the network: %v", rec.reached)
	}

	// The fields this integration does read are not caught by the fragments.
	for _, fields := range []string{extensionFields, extensionListFields, trunkFields, deviceFields, callFields, systemStatusFields, groupFields, inboundRuleFields} {
		if err := checkNames(fields, "$select"); err != nil {
			t.Errorf("a field this integration reads is refused: %v", err)
		}
	}
	if err := checkExpand(extensionExpand); err != nil {
		t.Errorf("the extension expand this integration uses is refused: %v", err)
	}
}

// An expanded property must carry its own $select, one level down and every
// level below that: Phones without one returns each handset's provisioning link.
func TestGuard_ExpandNeedsItsOwnSelect(t *testing.T) {
	c, _ := guarded(t)
	for _, exp := range []string{
		"Phones",
		"Phones($top=1)",
		"Groups($select=Name;$expand=Rights)",
		"Phones($select=Id),ForwardingProfiles",
	} {
		u := "https://pbx.example/xapi/v1/Users?$select=Id&$expand=" + url.QueryEscape(exp)
		err := try(t, c, http.MethodGet, u)
		if err == nil || !strings.Contains(err.Error(), "$select of its own") {
			t.Errorf("$expand=%s should be refused, got %v", exp, err)
		}
	}
	for _, exp := range []string{
		"Phones($select=Id,Name)",
		"Groups($select=Name;$expand=Rights($select=RoleName)),Phones($select=Id)",
		"Members($select=Id,Number,Name)",
	} {
		u := "https://pbx.example/xapi/v1/Users?$select=Id&$expand=" + url.QueryEscape(exp)
		if err := try(t, c, http.MethodGet, u); err != nil {
			t.Errorf("$expand=%s should be permitted: %v", exp, err)
		}
	}
}

// A request to any host but the configured one is refused, and a redirect is
// not followed. Either could carry the bearer token -- a credential for the
// whole PBX -- somewhere the operator never named.
func TestGuard_StaysOnTheConfiguredHost(t *testing.T) {
	c, rec := guarded(t)
	for _, u := range []string{
		"https://other.example/xapi/v1/Users?$select=Id",
		"http://pbx.example/xapi/v1/Users?$select=Id",
		"https://pbx.example.evil/xapi/v1/Users?$select=Id",
	} {
		err := try(t, c, http.MethodGet, u)
		if err == nil || !strings.Contains(err.Error(), "not the configured phone system") {
			t.Errorf("%s should be refused as another host, got %v", u, err)
		}
	}
	if len(rec.reached) != 0 {
		t.Errorf("a request to another host reached the network: %v", rec.reached)
	}
	if c.CheckRedirect == nil || c.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Error("the client should refuse to follow redirects")
	}
}

func TestSplitTopLevel(t *testing.T) {
	got, err := splitTopLevel("A($select=x,y;$expand=B($select=z)),C($select=d),E", ',')
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"A($select=x,y;$expand=B($select=z))", "C($select=d)", "E"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %v, want %v", got, want)
	}
	if _, err := splitTopLevel("A($select=x", ','); err == nil {
		t.Error("an unbalanced expand should be refused")
	}
}
