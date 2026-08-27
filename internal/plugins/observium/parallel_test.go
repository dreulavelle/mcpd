package observium

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

// TestCapacityFetchesItsCollectionsTogether proves the composite reads overlap.
//
// A barrier is the only honest test of this. Timing one is a benchmark rather
// than an assertion, and a mock recording the order of calls says nothing about
// whether they waited on each other. So every request blocks until all three
// have arrived: sequential fetches deadlock, and the deadlock is the failure.
//
// It is worth having because the win depends on the rate limiter's interval and
// the upstream's latency, which means the code can quietly stop being parallel
// and still look right in every other test.
func TestCapacityFetchesItsCollectionsTogether(t *testing.T) {
	const want = 3
	var arrived atomic.Int32
	var once sync.Once
	release := make(chan struct{})

	p := toolPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		if arrived.Add(1) == want {
			once.Do(func() { close(release) })
		}
		select {
		case <-release:
		case <-time.After(5 * time.Second):
			t.Errorf("only %d of %d requests were in flight at once; the "+
				"collections are being fetched one after another", arrived.Load(), want)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","count":0,%q:{}}`, envelopeKey(r.URL.Path))
	})
	// The limiter still spaces the requests; without headroom the barrier waits
	// out its own interval rather than testing anything.
	p.cfg.RequestsPerSecond = 100
	p.client.limiter = rate.NewLimiter(100, 1)

	if _, err := p.getCapacity(context.Background(), capacityArgs{}); err != nil {
		t.Fatalf("getCapacity: %v", err)
	}
	if got := arrived.Load(); got != want {
		t.Errorf("made %d requests, want %d", got, want)
	}
}

// TestTopologyKeepsWhatItGotWhenVLANsAreRefused is the case parallelism could
// easily have broken.
//
// VLANs need a level 7 account, and a refusal is not a failure of the call. Run
// in a group, returning that error would cancel the derived context and take
// the neighbours down with it -- turning a partial answer into no answer, which
// is the opposite of what the tolerance was for.
func TestTopologyKeepsWhatItGotWhenVLANsAreRefused(t *testing.T) {
	p := toolPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "vlans") {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"status":"failed","message":"insufficient level"}`)
			return
		}
		fmt.Fprintf(w, `{"status":"ok","count":1,%q:{"1":{"device_id":1}}}`,
			envelopeKey(r.URL.Path))
	})

	got, err := p.getTopology(context.Background(), topologyArgs{VLANs: true})
	if err != nil {
		t.Fatalf("a VLAN refusal must not fail the whole call: %v", err)
	}
	if got.VLANs.Note == "" {
		t.Error("the VLAN refusal was not reported")
	}
	if got.Neighbours.Count == 0 {
		t.Error("the neighbours were lost with the VLANs, which is the " +
			"cancellation this arrangement exists to avoid")
	}
}

// envelopeKey is the key the client expects a collection under, read from the
// route table rather than restated. Observium answers each endpoint under its
// own noun, and a test that hardcoded them would fail on the next route added
// for a reason that has nothing to do with what it tests.
func envelopeKey(requestPath string) string {
	path := strings.TrimPrefix(requestPath, apiPrefix)
	for _, route := range apiPaths {
		if route.path == path {
			return route.key
		}
	}
	return "entries"
}
