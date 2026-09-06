package backup

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// davRequest is what the fake server saw.
type davRequest struct {
	method string
	path   string
	depth  string
	// length is what the request declared, which for an upload is the whole
	// point: several WebDAV servers refuse a chunked body.
	length   int64
	user     string
	password string
}

// davServer is a WebDAV server that answers what the test told it to.
//
// A fake rather than a Nextcloud in a container. What is worth defending here
// is which requests mcpd makes and what it does with the answers, and a real
// server would be testing the server.
type davServer struct {
	mu       sync.Mutex
	requests []davRequest
	// propfind is the multistatus body, and status a code every verb gets
	// instead of the ordinary one. Zero means answer normally.
	propfind string
	status   int
}

func (d *davServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	user, password, _ := r.BasicAuth()
	d.mu.Lock()
	d.requests = append(d.requests, davRequest{
		method: r.Method, path: r.URL.Path, depth: r.Header.Get("Depth"),
		length: r.ContentLength, user: user, password: password,
	})
	status := d.status
	body := d.propfind
	d.mu.Unlock()

	io.Copy(io.Discard, r.Body) //nolint:errcheck // a fake

	if status != 0 {
		w.WriteHeader(status)
		return
	}
	switch r.Method {
	case "PROPFIND":
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusMultiStatus)
		io.WriteString(w, body) //nolint:errcheck // a fake
	case http.MethodDelete:
		// 204, which is what the specification says a delete with no body
		// answers and what real servers send.
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusCreated)
	}
}

func (d *davServer) seen() []davRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]davRequest(nil), d.requests...)
}

// davResponse builds one entry of a multistatus document.
func davResponse(href string, size int, collection bool) string {
	kind := "<D:resourcetype/>"
	if collection {
		kind = "<D:resourcetype><D:collection/></D:resourcetype>"
	}
	return `<D:response><D:href>` + href + `</D:href><D:propstat><D:prop>` +
		`<D:getcontentlength>` + strconv.Itoa(size) + `</D:getcontentlength>` +
		`<D:getlastmodified>Sun, 04 Jan 2026 04:00:00 GMT</D:getlastmodified>` +
		kind + `</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`
}

func davMultistatus(responses ...string) string {
	return `<?xml version="1.0" encoding="utf-8"?><D:multistatus xmlns:D="DAV:">` +
		strings.Join(responses, "") + `</D:multistatus>`
}

// openTestWebDAV starts a fake and points a transport at it.
func openTestWebDAV(t *testing.T, server *davServer) Transport {
	t.Helper()
	srv := httptest.NewServer(server)
	t.Cleanup(srv.Close)

	transport, err := OpenDestination(Destination{
		Kind: KindWebDAV,
		Settings: Settings{
			URL: srv.URL + "/backups", Username: "ops",
			// httptest speaks plain HTTP, which a destination only permits when
			// it has been told to. The refusal itself is tested in
			// TestWebDAVRefusesAPlainAddressUnlessItIsAllowed.
			AllowInsecure: true,
		},
		Secret: "hunter2",
	}, TransportOptions{})
	if err != nil {
		t.Fatalf("open the destination: %v", err)
	}
	t.Cleanup(func() { transport.Close() })
	return transport
}

// An upload declares its length rather than sending a chunked body.
//
// Several WebDAV servers, Synology's among them, refuse a chunked PUT. The
// length is known because the archive was spooled to disk before any of this
// ran, so there is no reason not to say it.
func TestWebDAVPutDeclaresALengthAndAuthenticates(t *testing.T) {
	server := &davServer{}
	transport := openTestWebDAV(t, server)

	const name = "mcpd-nas-20260104T040000Z.mcpdbak"
	const contents = "an archive, pretend"
	err := transport.Put(t.Context(), name, strings.NewReader(contents), int64(len(contents)))
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	seen := server.seen()
	if len(seen) != 1 {
		t.Fatalf("made %d requests, want one PUT", len(seen))
	}
	got := seen[0]
	if got.method != http.MethodPut {
		t.Errorf("method %s, want PUT", got.method)
	}
	if got.path != "/backups/"+name {
		t.Errorf("path %q, want /backups/%s", got.path, name)
	}
	if got.length != int64(len(contents)) {
		t.Errorf("declared %d bytes, want %d -- a chunked body is refused by "+
			"several WebDAV servers", got.length, len(contents))
	}
	if got.user != "ops" || got.password != "hunter2" {
		t.Errorf("signed in as %q/%q", got.user, got.password)
	}
}

// A listing carries only mcpd's own archives, and never the folder itself.
//
// A WebDAV collection lists itself at depth 1. Returning it as an object would
// have retention consider deleting the folder the backups are in.
func TestWebDAVListReturnsOnlyOurOwnArchivesAndNotTheFolder(t *testing.T) {
	server := &davServer{propfind: davMultistatus(
		davResponse("/backups/", 0, true),
		davResponse("/backups/mcpd-nas-20260104T040000Z.mcpdbak", 1024, false),
		davResponse("/backups/mcpd-nas-20260111T040000Z.mcpdbak", 2048, false),
		davResponse("/backups/readme.txt", 12, false),
		// A directory somebody named like an archive is still a directory.
		davResponse("/backups/mcpd-nas-20260118T040000Z.mcpdbak/", 0, true),
	)}
	transport := openTestWebDAV(t, server)

	objects, err := transport.List(t.Context())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(objects) != 2 {
		names := make([]string, 0, len(objects))
		for _, o := range objects {
			names = append(names, o.Name)
		}
		t.Fatalf("list returned %v, want the two archives and nothing else", names)
	}
	if objects[0].Name != "mcpd-nas-20260104T040000Z.mcpdbak" || objects[0].Size != 1024 {
		t.Errorf("first object %+v", objects[0])
	}
	if objects[1].Size != 2048 {
		t.Errorf("second object %+v", objects[1])
	}
	want := time.Date(2026, time.January, 4, 4, 0, 0, 0, time.UTC)
	if !objects[0].ModTime.Equal(want) {
		t.Errorf("modified %s, want %s", objects[0].ModTime, want)
	}

	seen := server.seen()
	if len(seen) != 1 || seen[0].method != "PROPFIND" {
		t.Fatalf("made %d requests, first %+v", len(seen), seen[0])
	}
	// Depth 1 rather than infinity: a folder somebody also keeps other things
	// in should not have its whole tree walked on every run.
	if seen[0].depth != "1" {
		t.Errorf("Depth %q, want 1", seen[0].depth)
	}
}

// An href arrives percent-encoded from most servers, and the name has to come
// back out of it -- retention reads the date out of that name.
func TestWebDAVListUnescapesHrefs(t *testing.T) {
	server := &davServer{propfind: davMultistatus(
		davResponse("/my%20backups/mcpd%2Dnas%2D20260104T040000Z%2Emcpdbak", 1024, false),
	)}
	objects, err := openTestWebDAV(t, server).List(t.Context())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(objects) != 1 || objects[0].Name != "mcpd-nas-20260104T040000Z.mcpdbak" {
		t.Fatalf("list returned %+v", objects)
	}
}

// A redirect is not followed.
//
// Following one would re-send the user name and password to whatever address
// the server named, which is not something an operator agreed to when they
// typed one.
func TestWebDAVRefusesARedirect(t *testing.T) {
	elsewhere := &davServer{}
	elsewhereSrv := httptest.NewServer(elsewhere)
	defer elsewhereSrv.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhereSrv.URL+"/stolen", http.StatusFound)
	}))
	defer redirector.Close()

	transport, err := OpenDestination(Destination{
		Kind: KindWebDAV,
		Settings: Settings{
			URL: redirector.URL + "/backups", Username: "ops", AllowInsecure: true,
		},
		Secret: "hunter2",
	}, TransportOptions{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer transport.Close()

	err = transport.Put(t.Context(), "mcpd-nas-20260104T040000Z.mcpdbak",
		strings.NewReader("x"), 1)
	if err == nil {
		t.Fatal("a redirect was followed")
	}
	if got := elsewhere.seen(); len(got) != 0 {
		t.Errorf("the credential reached %d requests at the redirect target: %+v", len(got), got)
	}
}

// The sentence a person reads carries no evidence, and the evidence is
// available separately.
//
// docs/dashboard-copy.md is the rule: a status code, a route or a header name
// goes under Technical details rather than into prose.
func TestWebDAVReportsWhatTheServerSaidWithoutPuttingItInTheSentence(t *testing.T) {
	server := &davServer{status: http.StatusUnauthorized}
	transport := openTestWebDAV(t, server)

	err := transport.Put(t.Context(), "mcpd-nas-20260104T040000Z.mcpdbak",
		strings.NewReader("x"), 1)
	if err == nil {
		t.Fatal("a 401 was accepted as a successful upload")
	}

	var written Evidencer
	if !errors.As(err, &written) {
		t.Fatalf("got %v, which carries no evidence of its own", err)
	}
	sentence := written.Error()
	if !strings.Contains(strings.ToLower(sentence), "user name and password") {
		t.Errorf("sentence %q does not say what an operator should look at", sentence)
	}
	for _, evidence := range []string{"401", "PUT", "http"} {
		if strings.Contains(sentence, evidence) {
			t.Errorf("sentence %q carries evidence: %q", sentence, evidence)
		}
	}
	if !strings.Contains(written.Evidence(), "401") ||
		!strings.Contains(written.Evidence(), "PUT") {
		t.Errorf("evidence %q does not say what was asked or what came back", written.Evidence())
	}
}

// Test connection writes as well as reads.
//
// A credential with read on the share and no write lists perfectly and fails at
// four in the morning. The probe's name is deliberately not one TimeFromName
// parses, so one left behind by an interrupted check can never be counted as a
// backup or deleted as one.
func TestWebDAVCheckWritesAndRemovesAProbe(t *testing.T) {
	server := &davServer{propfind: davMultistatus(davResponse("/backups/", 0, true))}
	if err := openTestWebDAV(t, server).Check(t.Context()); err != nil {
		t.Fatalf("check: %v", err)
	}

	var methods []string
	for _, r := range server.seen() {
		methods = append(methods, r.method+" "+r.path)
	}
	want := []string{
		"PROPFIND /backups/",
		"PUT /backups/.mcpd-check",
		"DELETE /backups/.mcpd-check",
	}
	if len(methods) != len(want) {
		t.Fatalf("made %v, want %v", methods, want)
	}
	for i := range want {
		if methods[i] != want[i] {
			t.Fatalf("made %v, want %v", methods, want)
		}
	}
	if _, ours := TimeFromName(".mcpd-check"); ours {
		t.Error("the probe's name parses as an archive, so retention could delete it")
	}
}

// A plain HTTP address is refused unless somebody ticked the box.
//
// The archive is encrypted; the password authenticating the upload is not, and
// it crosses the network on every run.
func TestWebDAVRefusesAPlainAddressUnlessItIsAllowed(t *testing.T) {
	d := Destination{
		Name: "nas", Kind: KindWebDAV, Policy: DefaultPolicy,
		Settings: Settings{URL: "http://nas.example.com/backups", Username: "ops"},
	}
	if err := d.Validate(""); err == nil {
		t.Error("an unencrypted address was accepted without anybody saying so")
	}

	d.Settings.AllowInsecure = true
	if err := d.Validate(""); err != nil {
		t.Errorf("an unencrypted address was refused after it had been allowed: %v", err)
	}
}

// A credential in the address ends up in a log line, in a copied link, and in
// this row's own configuration column.
func TestWebDAVRefusesCredentialsInTheAddress(t *testing.T) {
	d := Destination{
		Name: "nas", Kind: KindWebDAV, Policy: DefaultPolicy,
		Settings: Settings{URL: "https://ops:hunter2@nas.example.com/backups"},
	}
	err := d.Validate("")
	if err == nil {
		t.Fatal("an address carrying a password was accepted")
	}
	if !strings.Contains(err.Error(), "own boxes") {
		t.Errorf("the refusal does not say where the password belongs: %v", err)
	}
}
