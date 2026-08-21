package cnmaestro

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// recordingTransport stands in for the network and remembers what reached it.
type recordingTransport struct {
	got *http.Request
}

func (r *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.got = req
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"data":[]}`)),
		Header:     http.Header{},
		Request:    req,
	}, nil
}

// The guard is what makes "this integration cannot write" true, rather than
// true by inspection of the tools that happen to exist today. cnMaestro is
// production network infrastructure for the people running it, and the
// endpoints on the deny-list execute commands on live devices.
func TestReadOnlyTransport(t *testing.T) {
	tests := []struct {
		name    string
		method  string
		url     string
		wantErr string
	}{
		{
			name:   "an ordinary read passes",
			method: http.MethodGet,
			url:    "https://example.test/api/v2/devices?limit=100",
		},
		{
			name:   "the token endpoint may be posted to",
			method: http.MethodPost,
			url:    "https://example.test" + tokenPath,
		},
		{
			name:    "a write is refused whatever it addresses",
			method:  http.MethodPost,
			url:     "https://example.test/api/v2/devices/AA:BB:CC:DD:EE:FF",
			wantErr: "only reads",
		},
		{
			name:    "a delete is refused",
			method:  http.MethodDelete,
			url:     "https://example.test/api/v2/networks/default",
			wantErr: "only reads",
		},
		{
			name:    "a post to something merely resembling the token path is refused",
			method:  http.MethodPost,
			url:     "https://example.test/api/v2/access/token/../devices/x/cli",
			wantErr: "only reads",
		},
		{
			name:    "remote command execution is refused",
			method:  http.MethodGet,
			url:     "https://example.test/api/v2/devices/AA:BB:CC:DD:EE:FF/cli",
			wantErr: "deny-list",
		},
		{
			// The bug this exists for. Checking the path where it was built
			// compared a string the server never sees: an escaped separator
			// slips past an anchored pattern, and the server decodes it back
			// into the endpoint that runs commands on an access point.
			name:    "an escaped separator does not smuggle a blocked path through",
			method:  http.MethodGet,
			url:     "https://example.test/api/v2/devices/AA%2Fcli",
			wantErr: "deny-list",
		},
		{
			name:    "a reboot is refused",
			method:  http.MethodGet,
			url:     "https://example.test/api/v2/devices/AA:BB:CC:DD:EE:FF/reboot",
			wantErr: "deny-list",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := &recordingTransport{}
			client := readOnly(&http.Client{Transport: base})

			req, err := http.NewRequest(tc.method, tc.url, nil)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			resp, err := client.Do(req)
			if resp != nil {
				resp.Body.Close()
			}

			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Do: %v, want it to pass", err)
				}
				if base.got == nil {
					t.Fatal("the request never reached the network")
				}
				return
			}
			if err == nil {
				t.Fatalf("the request was allowed through; want %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to mention %q", err, tc.wantErr)
			}
			if base.got != nil {
				t.Errorf("a refused request still reached the network: %s %s",
					base.got.Method, base.got.URL)
			}
		})
	}
}

// A nil client is what a plugin gets when the host supplied none, and it must
// still be guarded rather than silently unwrapped.
func TestReadOnly_GuardsANilClient(t *testing.T) {
	c := readOnly(nil)
	if _, ok := c.Transport.(readOnlyTransport); !ok {
		t.Fatalf("transport = %T, want the guard", c.Transport)
	}
}

// The guard belongs to this plugin, not to whatever else shares the host's
// client. Mutating the caller's client would apply a read-only rule to every
// other user of it.
func TestReadOnly_DoesNotMutateTheCallersClient(t *testing.T) {
	base := &recordingTransport{}
	original := &http.Client{Transport: base}

	readOnly(original)

	if original.Transport != http.RoundTripper(base) {
		t.Errorf("the caller's client was modified")
	}
}
