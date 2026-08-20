package oauth

import (
	"html/template"
	"net/http"
	"strings"
)

// The consent screen is the one place a human decides what an agent may do, so
// it has to state that plainly: which client is asking, which plugins it will
// reach, and whether it will be able to propose changes.
//
// html/template escapes every interpolated value, which matters because the
// client name arrives from an unauthenticated registration request. It is
// additionally filtered to printable ASCII by sanitizeDisplay, so it cannot
// carry direction-override characters or newlines onto the page.
var consentTmpl = template.Must(template.New("consent").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Authorize {{.ClientName}}</title>
<style>
  :root { color-scheme: light dark; --fg:#151b1a; --bg:#f6f7f5; --card:#fff;
          --rule:#d6dcd9; --muted:#5c6764; --accent:#0f6e5c; --warn:#8a5a0c; }
  @media (prefers-color-scheme:dark) {
    :root { --fg:#e7ecea; --bg:#0e1413; --card:#151c1b; --rule:#2b3735;
            --muted:#93a09c; --accent:#4fbba2; --warn:#d7a252; }
  }
  * { box-sizing:border-box }
  body { margin:0; min-height:100vh; display:grid; place-items:center;
         background:var(--bg); color:var(--fg); padding:24px;
         font:16px/1.6 ui-sans-serif,system-ui,-apple-system,sans-serif }
  .card { width:100%; max-width:26rem; background:var(--card);
          border:1px solid var(--rule); border-radius:6px; padding:28px }
  h1 { font-size:20px; margin:0 0 6px; letter-spacing:-.01em }
  .sub { color:var(--muted); font-size:14px; margin:0 0 22px }
  .grants { border:1px solid var(--rule); border-radius:4px; padding:14px 16px;
            margin:0 0 22px; font-size:14px }
  .grants h2 { font-size:11px; letter-spacing:.1em; text-transform:uppercase;
               color:var(--muted); margin:0 0 10px; font-weight:600 }
  ul { margin:0; padding-left:18px } li { margin-bottom:5px }
  .warn { color:var(--warn); font-weight:500 }
  label { display:block; font-size:13px; color:var(--muted); margin-bottom:5px }
  input[type=text],input[type=password] {
    width:100%; padding:9px 11px; margin-bottom:14px; font-size:15px;
    border:1px solid var(--rule); border-radius:4px;
    background:var(--bg); color:var(--fg) }
  .row { display:flex; gap:10px; margin-top:6px }
  button { flex:1; padding:10px; font-size:15px; font-weight:500;
           border-radius:4px; cursor:pointer; border:1px solid var(--rule) }
  .primary { background:var(--accent); color:#fff; border-color:var(--accent) }
  .secondary { background:transparent; color:var(--fg) }
  .err { background:#a33a2c14; color:#a33a2c; border:1px solid #a33a2c40;
         padding:10px 12px; border-radius:4px; font-size:14px; margin-bottom:18px }
  .who { font-size:13px; color:var(--muted); margin-bottom:14px }
  :focus-visible { outline:2px solid var(--accent); outline-offset:2px }
</style>
</head>
<body>
<div class="card">
  <h1>Authorize {{.ClientName}}</h1>
  <p class="sub">{{.ClientName}} is requesting access to your mcpd host.</p>

  {{if .Error}}<div class="err">{{.Error}}</div>{{end}}

  <div class="grants">
    <h2>This will allow it to</h2>
    <ul>
      {{range .Grants}}<li{{if .Elevated}} class="warn"{{end}}>{{.Text}}</li>{{end}}
    </ul>
  </div>

  <form method="POST" action="/oauth/authorize">
    {{range $k, $v := .Hidden}}<input type="hidden" name="{{$k}}" value="{{$v}}">{{end}}

    {{if .User}}
      <p class="who">Signed in as <strong>{{.User}}</strong></p>
    {{else}}
      <label for="username">Username</label>
      <input id="username" name="username" type="text" autocomplete="username"
             autofocus required>
      <label for="password">Password</label>
      <input id="password" name="password" type="password"
             autocomplete="current-password" required>
    {{end}}

    <div class="row">
      <button class="secondary" type="submit" name="action" value="deny">Deny</button>
      <button class="primary" type="submit" name="action" value="allow">Allow</button>
    </div>
  </form>
</div>
</body>
</html>`))

var errorTmpl = template.Must(template.New("error").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Authorization error</title>
<style>
  :root { color-scheme: light dark; --fg:#151b1a; --bg:#f6f7f5; --card:#fff;
          --rule:#d6dcd9; --muted:#5c6764; --alarm:#a33a2c; }
  @media (prefers-color-scheme:dark) {
    :root { --fg:#e7ecea; --bg:#0e1413; --card:#151c1b; --rule:#2b3735;
            --muted:#93a09c; --alarm:#e08472; }
  }
  body { margin:0; min-height:100vh; display:grid; place-items:center;
         background:var(--bg); color:var(--fg); padding:24px;
         font:16px/1.6 ui-sans-serif,system-ui,-apple-system,sans-serif }
  .card { max-width:26rem; background:var(--card); border:1px solid var(--rule);
          border-radius:6px; padding:28px }
  h1 { font-size:19px; margin:0 0 10px; color:var(--alarm) }
  p { margin:0; color:var(--muted); font-size:14px }
  code { font-family:ui-monospace,monospace; font-size:13px }
</style>
</head>
<body>
<div class="card">
  <h1>Authorization could not continue</h1>
  <p>{{.Description}}</p>
  <p style="margin-top:12px"><code>{{.Code}}</code></p>
</div>
</body>
</html>`))

// grant is one line on the consent screen.
type grant struct {
	Text string
	// Elevated marks a permission that changes infrastructure, so it is
	// visually distinct from read access.
	Elevated bool
}

type consentData struct {
	ClientName string
	User       string
	Error      string
	Grants     []grant
	Hidden     map[string]string
}

// renderConsent draws the login and consent page.
func (s *Server) renderConsent(w http.ResponseWriter, req *authorizeRequest, user *User, errMsg string) {
	data := consentData{
		ClientName: req.client.Name,
		Error:      errMsg,
		Hidden: map[string]string{
			"client_id":             req.ClientID,
			"redirect_uri":          req.RedirectURI,
			"response_type":         "code",
			"scope":                 req.Scope,
			"state":                 req.State,
			"code_challenge":        req.CodeChallenge,
			"code_challenge_method": req.CodeChallengeMethod,
		},
	}
	if user != nil {
		data.User = user.Username
		data.Grants = describeGrants(s.grantScope(req.Scope, user))
	} else {
		data.Grants = describeGrants(req.Scope)
	}

	// The consent page carries the authorization parameters and must never be
	// cached or framed: clickjacking here would let a page trick a user into
	// authorizing a client they cannot see.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	_ = consentTmpl.Execute(w, data)
}

// describeGrants turns a scope string into language a person can act on.
func describeGrants(scope string) []grant {
	var out []grant
	plugins := PluginsFromScope(scope)

	switch {
	case len(plugins) == 0:
		out = append(out, grant{Text: "Reach no integrations (no plugin access was granted)"})
	case contains(plugins, "*"):
		out = append(out, grant{Text: "Reach every integration on this host", Elevated: true})
	default:
		out = append(out, grant{
			Text: "Reach only these integrations: " + strings.Join(plugins, ", "),
		})
	}

	if HasScope(scope, ScopeRead) {
		out = append(out, grant{Text: "Read data from those integrations"})
	}
	if HasScope(scope, ScopePropose) {
		out = append(out, grant{
			Text:     "Propose changes, which stay pending until someone approves them",
			Elevated: true,
		})
	}
	if HasScope(scope, ScopeApprove) {
		out = append(out, grant{
			Text:     "Approve changes, allowing them to take effect",
			Elevated: true,
		})
	}
	return out
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// renderErrorPage shows an authorization error that must not be redirected.
func (s *Server) renderErrorPage(w http.ResponseWriter, e *Error) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	w.WriteHeader(e.Status())
	_ = errorTmpl.Execute(w, e)
}
