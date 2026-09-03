package threecx

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// guard is the last thing every request passes through, and the only place the
// read-only guarantee and the no-credentials guarantee are actually enforced.
//
// # Why an allow-list rather than a method check
//
// A method check -- "GET only" -- would cover the read-only half today. It
// would not cover the other half: GET /xapi/v1/Users with no $select answers
// with every extension's SIP password, and GET /xapi/v1/SipDevices answers with
// every handset's web password however it is asked. A guarantee that permits a
// credential dump is not the guarantee it says it is, so a request is refused
// unless its method and its path are both named below, and a read is refused
// unless it names the fields it wants and none of them is a credential.
//
// Default-deny is the stronger guarantee. Adding a tool that reaches a new
// endpoint means naming that endpoint here, in front of this comment, which is
// the amount of friction the decision deserves against somebody's live PBX.
//
// # The one write
//
// Signing in is a POST: the extension's password is exchanged for a bearer
// token at /webclient/api/Login/GetAccessToken. It is outside the OData root,
// it changes nothing, and it is the only POST permitted.
type guard struct {
	base http.RoundTripper
	// scheme and host are the configured phone system. A request to anywhere
	// else is refused outright: the only way to produce one is a redirect
	// chased somewhere else or a bug in how a URL was built, and either would
	// carry the bearer token -- a credential for the whole PBX -- to a host
	// the operator never named.
	scheme, host string
}

// rule is one read this integration may make, as the path under /xapi/v1/.
type rule struct {
	path *regexp.Regexp
	// why is quoted back in a refusal, so a reader of the error learns what the
	// path is for rather than only that it is not allowed.
	why string
	// raw marks the one endpoint that is a file rather than an entity: the
	// support bundle zip. There is nothing to project, so $select is not
	// required of it -- and nothing else may claim this exemption.
	raw bool
}

// allowed is the complete set of OData reads this integration may make.
// Everything else under the API root is refused before it reaches the network.
//
// Every entry is a GET. Grouped by the question a tool asks.
var allowed = []rule{
	// What state the system is in.
	{regexp.MustCompile(`^SystemStatus$`), "reading the system's status", false},
	{regexp.MustCompile(`^LicenseStatus$`), "reading the licence", false},
	{regexp.MustCompile(`^Services$`), "listing the system's own services", false},
	{regexp.MustCompile(`^ActiveCalls$`), "listing calls in progress", false},
	{regexp.MustCompile(`^EventLogs$`), "searching the event log", false},

	// Extensions and the handsets on them.
	{regexp.MustCompile(`^Users$`), "listing extensions", false},
	{regexp.MustCompile(`^Users\(\d+\)$`), "reading one extension", false},
	{regexp.MustCompile(`^DeviceInfos$`), "listing provisioned handsets", false},

	// Where calls come in and go out.
	{regexp.MustCompile(`^Trunks$`), "listing trunks and their numbers", false},
	{regexp.MustCompile(`^InboundRules$`), "listing where each number rings", false},
	{regexp.MustCompile(`^OutboundRules$`), "listing outbound dialling rules", false},
	{regexp.MustCompile(`^Peers$`), "listing everything that has a number", false},

	// The things a call can land on.
	{regexp.MustCompile(`^RingGroups$`), "listing ring groups", false},
	{regexp.MustCompile(`^Queues$`), "listing call queues", false},
	{regexp.MustCompile(`^Receptionists$`), "listing digital receptionists", false},

	// When the business is open.
	{regexp.MustCompile(`^Groups$`), "listing departments", false},
	{regexp.MustCompile(`^Groups\(\d+\)$`), "reading one department's schedule", false},
	{regexp.MustCompile(`^Defs/TimeZones$`), "naming a time zone", false},

	// What happened.
	{regexp.MustCompile(`^CallHistoryView$`), "searching call records", false},

	// The support bundle: the same zip the console's "collect support info"
	// button produces. A file, not an entity, so it is the one read that
	// carries no $select; it is read into a digest and never returned whole.
	{regexp.MustCompile(`^SupportInfo$`), "collecting a support bundle", true},
}

// refusedFields are property names that must never be requested, whatever
// entity they sit on. Matched case-insensitively against every name in a
// $select, a nested $select inside $expand, a $filter and an $orderby.
//
// Each is a credential or leads to one. AuthID, AuthPassword and SIPID are an
// extension's SIP registration; DeskphonePassword and AccessPassword open the
// handset's web page and the console; VMPIN opens the voicemail box; the
// provisioning links and Parameters carry a URL a handset fetches its whole
// configuration -- credentials included -- from; LicenseKey is the licence;
// Certificate and SeparateAuthId are a trunk's; Settings on a phone carries
// Secret; Registrar carries InterfaceLink.
var refusedFields = map[string]bool{
	"authid": true, "authpassword": true, "accesspassword": true,
	"deskphonepassword": true, "phonewebpassword": true, "vmpin": true,
	"sipid": true, "licensekey": true, "password": true, "secret": true,
	"provlink": true, "provisioninglinklocal": true, "provisioninglinkext": true,
	"provisionlink": true, "interfacelink": true, "parameters": true,
	"networkpath": true, "certificate": true, "separateauthid": true,
	"settings": true, "registrar": true, "downloadlink": true,
	"recordingurl": true, "archivedurl": true, "contactimage": true,
}

// refusedFragments catch a property this list has never heard of by what it is
// called. A new 3CX build that adds ApiSecret or PairingKey should be refused
// by the shape of the name rather than waited for.
var refusedFragments = []string{"password", "secret", "provision", "link", "token", "key", "pin"}

// allowedDespiteFragment are properties a fragment would otherwise catch and
// that carry nothing sensitive. Listed by exact name, so the exception is as
// narrow as the field.
var allowedDespiteFragment = map[string]bool{
	// A boolean: whether the voicemail box asks for its PIN. Not the PIN.
	"pinprotected":      true,
	"pinprotecttimeout": true,
}

// refusedField reports whether a property name may not be requested.
func refusedField(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return false
	}
	if refusedFields[n] {
		return true
	}
	if allowedDespiteFragment[n] {
		return false
	}
	for _, fragment := range refusedFragments {
		if strings.Contains(n, fragment) {
			return true
		}
	}
	return false
}

func (g guard) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != g.scheme || !strings.EqualFold(req.URL.Host, g.host) {
		return nil, fmt.Errorf("3cx: refusing %s %s; it is not the configured "+
			"phone system (%s://%s), and a request elsewhere would carry its token",
			req.Method, req.URL.Redacted(), g.scheme, g.host)
	}

	path := normalisePath(req.URL.Path)

	// The sign-in, and nothing else that is not a read.
	if path == loginPath {
		if req.Method != http.MethodPost {
			return nil, fmt.Errorf("3cx: refusing %s %s; signing in is a POST", req.Method, path)
		}
		return g.roundTrip(req)
	}
	if req.Method != http.MethodGet {
		return nil, fmt.Errorf("3cx: refusing %s %s; this integration only reads "+
			"the phone system, and %s is not a read", req.Method, path, req.Method)
	}

	rest, ok := strings.CutPrefix(path, strings.TrimSuffix(apiPrefix, "/")+"/")
	if !ok {
		return nil, fmt.Errorf("3cx: refusing GET %s; it is not under the "+
			"configuration API root %s", path, apiPrefix)
	}
	var matched *rule
	for i := range allowed {
		if allowed[i].path.MatchString(rest) {
			matched = &allowed[i]
			break
		}
	}
	if matched == nil {
		return nil, fmt.Errorf("3cx: refusing GET %s; it is not one of the "+
			"endpoints this integration is permitted to read. Every request is "+
			"checked against an allow-list, so a read this plugin needs has to "+
			"be added to it deliberately", path)
	}

	if matched.raw {
		return g.roundTrip(req)
	}
	if err := checkProjection(req.URL.Query()); err != nil {
		return nil, fmt.Errorf("3cx: refusing GET %s (%s): %w", path, matched.why, err)
	}
	return g.roundTrip(req)
}

// checkProjection enforces the two rules about what a read may ask for: it
// must name its fields, and none of them may be a credential.
//
// $select is mandatory because 3CX's default projection of an extension
// includes its SIP password. The rule holds for every endpoint rather than the
// ones known to leak, because the list of ones known to leak is exactly the
// list somebody has already checked.
//
// Every expanded navigation property must carry its own $select, for the same
// reason one level down: $expand=Phones without one returns each handset's
// provisioning link.
func checkProjection(q url.Values) error {
	sel := strings.TrimSpace(q.Get("$select"))
	if sel == "" {
		return fmt.Errorf("the request names no $select; 3CX's default " +
			"projection includes credentials, so every read must say which " +
			"fields it wants")
	}
	if err := checkNames(sel, "$select"); err != nil {
		return err
	}
	if exp := strings.TrimSpace(q.Get("$expand")); exp != "" {
		if err := checkExpand(exp); err != nil {
			return err
		}
	}
	// A credential cannot be read through a comparison either.
	for _, key := range []string{"$filter", "$orderby"} {
		if v := q.Get(key); v != "" {
			for _, ident := range identifiers.FindAllString(v, -1) {
				if refusedField(ident) {
					return fmt.Errorf("%s refers to %s, which is a credential", key, ident)
				}
			}
		}
	}
	return nil
}

// identifiers finds the property-like words in a $filter or $orderby.
var identifiers = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// checkNames refuses a comma-separated field list carrying a credential.
func checkNames(list, where string) error {
	for _, name := range strings.Split(list, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			return fmt.Errorf("%s has an empty entry", where)
		}
		if refusedField(name) {
			return fmt.Errorf("%s names %s, which is a credential and is never read", where, name)
		}
	}
	return nil
}

// checkExpand walks an OData $expand -- Nav($select=a;$expand=Sub($select=b)),
// Other($select=c) -- requiring a $select on every expanded property and
// refusing a credential anywhere in it.
func checkExpand(exp string) error {
	items, err := splitTopLevel(exp, ',')
	if err != nil {
		return err
	}
	for _, item := range items {
		name, options := item, ""
		if i := strings.IndexByte(item, '('); i >= 0 {
			if !strings.HasSuffix(item, ")") {
				return fmt.Errorf("$expand %q is not balanced", item)
			}
			name, options = item[:i], item[i+1:len(item)-1]
		}
		name = strings.TrimSpace(name)
		if name == "" {
			return fmt.Errorf("$expand has an empty entry")
		}
		if refusedField(name) {
			return fmt.Errorf("$expand names %s, which carries credentials and is never read", name)
		}

		parts, err := splitTopLevel(options, ';')
		if err != nil {
			return err
		}
		selected := false
		for _, part := range parts {
			key, value, _ := strings.Cut(strings.TrimSpace(part), "=")
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "$select":
				selected = true
				if err := checkNames(value, "$expand="+name+"($select)"); err != nil {
					return err
				}
			case "$expand":
				if err := checkExpand(value); err != nil {
					return err
				}
			case "":
			default:
				// $filter, $orderby, $top inside an expand: the identifiers
				// still may not be credentials.
				for _, ident := range identifiers.FindAllString(value, -1) {
					if refusedField(ident) {
						return fmt.Errorf("$expand=%s(%s) refers to %s, which is a credential", name, key, ident)
					}
				}
			}
		}
		if !selected {
			return fmt.Errorf("$expand=%s carries no $select of its own; an expanded "+
				"property's default projection can include credentials, so it must "+
				"say which fields it wants", name)
		}
	}
	return nil
}

// splitTopLevel splits on a separator, ignoring separators inside parentheses.
func splitTopLevel(s string, sep byte) ([]string, error) {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return nil, fmt.Errorf("%q has an unmatched parenthesis", s)
			}
		case sep:
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	if depth != 0 {
		return nil, fmt.Errorf("%q has an unmatched parenthesis", s)
	}
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	return append(out, s[start:]), nil
}

func (g guard) roundTrip(req *http.Request) (*http.Response, error) {
	base := g.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// normalisePath puts a path into the single form the allow-list is written
// for, so a request cannot arrive past an anchored pattern by spelling.
//
// URL.Path is already percent-decoded, so a path reaching this check by a
// different spelling is compared in the form the server will route on.
func normalisePath(path string) string {
	p := strings.TrimSpace(path)
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
	}
	return p
}

// readOnly wraps a client so every request it makes goes through the guard.
//
// A copy: the host's HTTP client is shared, and a transport that refuses
// everything but a named list of reads belongs to this plugin rather than to
// everything using that client.
func readOnly(c *http.Client, root string) *http.Client {
	g := guard{}
	if u, err := url.Parse(root); err == nil {
		g.scheme, g.host = u.Scheme, u.Host
	}
	if c == nil {
		return &http.Client{Transport: g, CheckRedirect: dontFollow}
	}
	clone := *c
	g.base = c.Transport
	clone.Transport = g
	clone.CheckRedirect = dontFollow
	return &clone
}

// dontFollow stops the client chasing redirects, so a redirect arrives as a
// redirect rather than as whatever it eventually lands on.
//
// A 3CX behind a reverse proxy, or one whose FQDN has moved, answers with a
// redirect; following it would turn a diagnosable "the address is wrong" into
// an HTML page parsed as JSON. And a redirect is the one thing that could carry
// a request past the guard's host check to somewhere the bearer token should
// not go.
func dontFollow(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}
