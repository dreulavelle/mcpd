package observium

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func testClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	cfg := Config{Backend: BackendAPI, BaseURL: srv.URL, Token: "t"}
	cfg.withDefaults()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("test config invalid: %v", err)
	}
	return NewClient(srv.Client(), cfg, "t", "", "",
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		time.Now, nil, func(string, time.Duration) {}), srv
}

// Observium answers a collection as an object keyed by entity id, not as an
// array. Decoding it as a list fails outright; decoding it as a map loses the
// order, so the same call would return devices in a different sequence each
// time and a model comparing two answers would see changes that never
// happened.
func TestDecodeCollection_KeyedObjectIsOrderedByID(t *testing.T) {
	body := []byte(`{"status":"ok","count":3,"devices":{
		"10":{"device_id":"10","hostname":"c"},
		"2":{"device_id":"2","hostname":"a"},
		"9":{"device_id":"9","hostname":"b"}}}`)

	// Run it repeatedly: Go randomises map iteration, so a single pass can
	// pass by luck against an implementation that does not sort.
	for i := 0; i < 20; i++ {
		items, err := decodeCollection(body, "devices")
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		var got []string
		for _, item := range items {
			got = append(got, fmt.Sprint(item["hostname"]))
		}
		if strings.Join(got, ",") != "a,b,c" {
			t.Fatalf("order = %v, want a,b,c (numeric by id, not lexical)", got)
		}
	}
}

// PHP encodes an empty associative array as [], so an endpoint with no results
// answers with an array where a populated one answers with an object. Letting
// the object decode fail here would turn "no sensors" into an error.
func TestDecodeCollection_EmptyArrayIsNoResults(t *testing.T) {
	items, err := decodeCollection([]byte(`{"status":"ok","count":0,"sensors":[]}`), "sensors")
	if err != nil {
		t.Fatalf("an empty collection must not be an error: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("got %d items, want none", len(items))
	}
}

// A missing key is an empty result rather than a failure: some endpoints omit
// the collection entirely when nothing matched.
func TestDecodeCollection_MissingKeyIsEmpty(t *testing.T) {
	items, err := decodeCollection([]byte(`{"status":"ok","count":0}`), "ports")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("got %d items, want none", len(items))
	}
}

// Observium answers some failures with HTTP 200 and status "failed" in the
// body. A client that only checks the status code hands the model an empty
// collection, which reads as "there are none" rather than "you were refused".
func TestDo_TwoHundredSayingFailedIsAnError(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"failed","message":"insufficient privileges"}`)
	})

	_, err := c.walk(context.Background(), "/vlans", "vlans", url.Values{})
	if err == nil {
		t.Fatal("a 200 with status=failed must be an error, not an empty result")
	}
	if !strings.Contains(err.Error(), "insufficient privileges") {
		t.Fatalf("the upstream reason must survive: %v", err)
	}
}

// An HTML body means something other than the API answered -- a login page, a
// reverse proxy, a web server. Quoting the first 200 bytes of a <head> helps
// nobody; naming what happened does.
func TestDo_HTMLBodyIsExplained(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "<!DOCTYPE html><html><head><title>Sign in</title>")
	})

	_, err := c.walk(context.Background(), "/devices", "devices", url.Values{})
	if err == nil {
		t.Fatal("an HTML body is not a valid API response")
	}
	if !strings.Contains(err.Error(), "HTML page") {
		t.Fatalf("the error should say the response was HTML: %v", err)
	}
}

// Walking stops at MaxItems and says so. A model shown 3 of 3000 ports without
// being told will answer as though it saw the estate.
func TestWalk_TruncatesAndReportsIt(t *testing.T) {
	const total = 30
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		pageNo, _ := strconv.Atoi(r.URL.Query().Get("pageno"))
		size, _ := strconv.Atoi(r.URL.Query().Get("pagesize"))
		if pageNo < 1 {
			pageNo = 1
		}
		ports := map[string]any{}
		for i := 0; i < size; i++ {
			id := (pageNo-1)*size + i
			if id >= total {
				break
			}
			ports[strconv.Itoa(id)] = map[string]any{"port_id": id}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok", "count": total, "ports": ports,
		})
	})
	c.cfg.PageSize = 10
	c.cfg.MaxItems = 12

	page, err := c.walk(context.Background(), "/ports", "ports", url.Values{})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(page.Items) != 12 {
		t.Fatalf("got %d items, want the MaxItems ceiling of 12", len(page.Items))
	}
	if !page.Truncated {
		t.Fatal("a walk stopped by MaxItems must report itself truncated")
	}
	if page.Total != total {
		t.Fatalf("total = %d, want the upstream count %d", page.Total, total)
	}
}

// An endpoint that ignores pageno would otherwise return the same full page
// forever. The walk has to notice it has as many items as the upstream says
// exist and stop, rather than spinning to MaxItems making identical requests.
func TestWalk_StopsWhenUpstreamIgnoresPageNo(t *testing.T) {
	var calls int
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls > 5 {
			t.Error("the walk did not stop; it is looping on an endpoint that ignores pageno")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		devices := map[string]any{}
		for i := 0; i < 4; i++ {
			devices[strconv.Itoa(i)] = map[string]any{"device_id": i}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok", "count": 4, "devices": devices,
		})
	})
	c.cfg.PageSize = 4
	c.cfg.MaxItems = 1000

	page, err := c.walk(context.Background(), "/devices", "devices", url.Values{})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(page.Items) != 4 {
		t.Fatalf("got %d items, want 4", len(page.Items))
	}
}

// The bearer token belongs in a header. A credential in a query string ends up
// in the upstream's access log.
func TestClient_SendsBearerToken(t *testing.T) {
	var gotAuth, gotQuery string
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"ok","count":0,"devices":[]}`)
	})

	if _, err := c.walk(context.Background(), "/devices", "devices", url.Values{}); err != nil {
		t.Fatalf("walk: %v", err)
	}
	if gotAuth != "Bearer t" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer t")
	}
	if strings.Contains(gotQuery, "t") && strings.Contains(gotQuery, "token") {
		t.Errorf("the token must not reach the query string: %q", gotQuery)
	}
}
