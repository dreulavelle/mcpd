package flowroute

import (
	"errors"
	"strings"
	"testing"
)

// The two 404s mean different things. "No such port order" is an answer;
// "the requested URL was not found" means this package built a path Flowroute
// has never served, which is a bug here. Collapsing them would send somebody
// looking for a port order that was never missing.
func TestTheTwo404sAreNotTheSame(t *testing.T) {
	t.Parallel()

	resource := []byte(`{"errors":[{"detail":"No such port order","id":"2fae",
	  "status":404,"title":"Resource not found"}]}`)
	routing := []byte(`{"errors":[{"status":"404 Not Found: The requested URL was not ` +
		`found on the server. If you entered the URL manually please check your spelling."}]}`)

	err := explainRequestFailure(404, resource)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("a resource 404 should be ErrNotFound, got %v", err)
	}
	if errors.Is(err, ErrBadPath) {
		t.Fatal("a resource 404 is not a bad path")
	}

	err = explainRequestFailure(404, routing)
	if !errors.Is(err, ErrBadPath) {
		t.Fatalf("a routing 404 should be ErrBadPath, got %v", err)
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatal("a routing 404 must not read as an absent resource")
	}

	// A 404 that is not the envelope at all did not come from the API. The
	// safer reading is that the address is wrong.
	if err := explainRequestFailure(404, []byte("<html>nginx</html>")); !errors.Is(err, ErrBadPath) {
		t.Fatalf("an unparseable 404 should read as a bad path, got %v", err)
	}
}

// A rotated key stops every read at once, so the message has to say which
// credential and where to fix it.
func TestExplains401AsTheCredential(t *testing.T) {
	t.Parallel()
	err := explainRequestFailure(401, []byte(`{"errors":[{"status":401,
	  "title":"Unauthorized","detail":"Invalid credentials"}]}`))
	for _, want := range []string{"access key", "secret key", "Invalid credentials"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the 401 should mention %q, said %q", want, err)
		}
	}
}

// A validation failure sends its detail as an object keyed by field, where
// everything else sends a sentence. The field name is the whole of what makes
// a 422 actionable.
func TestRendersAFieldKeyedDetail(t *testing.T) {
	t.Parallel()
	err := explainRequestFailure(422, []byte(`{"errors":[{"detail":
	  {"start_date":["Missing data for required field."]},"id":"37df","status":422}]}`))
	if !strings.Contains(err.Error(), "start_date") {
		t.Fatalf("want the field named, said %q", err)
	}
	if !strings.Contains(err.Error(), "Missing data") {
		t.Fatalf("want the reason, said %q", err)
	}
}

// Something between here and Flowroute answering instead of the API is a
// different problem from Flowroute refusing, and the message should say so.
func TestNamesAnHTMLResponse(t *testing.T) {
	t.Parallel()
	got := summarise(200, []byte("<!DOCTYPE html><html><body>Sign in</body></html>"))
	if !strings.Contains(got, "HTML page") {
		t.Fatalf("want the HTML page named, said %q", got)
	}
}
