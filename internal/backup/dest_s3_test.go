package backup

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type s3Request struct {
	method string
	path   string
	query  url.Values
	// bytes is how long the object being written is, so an upload can be
	// checked to have carried the archive rather than merely to have happened.
	//
	// Read from x-amz-decoded-content-length rather than from the body, because
	// minio-go signs an upload chunk by chunk and the bytes on the wire are the
	// object wrapped in signatures.
	bytes int64
}

// fakeS3Server answers the handful of S3 operations this transport uses.
//
// A fake rather than a MinIO in a container: what is worth testing here is
// which requests minio-go is asked to make, and a real service would be testing
// the service. The unhandled case answers 404 with an S3-shaped error and
// records the path, so a failure says what was actually asked for.
type fakeS3Server struct {
	mu       sync.Mutex
	requests []s3Request
}

var _ http.Handler = (*fakeS3Server)(nil)

func (s *fakeS3Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	io.Copy(io.Discard, r.Body) //nolint:errcheck // a fake; a short read fails the assertion below
	declared := r.ContentLength
	if decoded := r.Header.Get("x-amz-decoded-content-length"); decoded != "" {
		declared, _ = strconv.ParseInt(decoded, 10, 64)
	}
	s.mu.Lock()
	s.requests = append(s.requests, s3Request{
		method: r.Method, path: r.URL.Path, query: query, bytes: declared,
	})
	s.mu.Unlock()

	switch {
	case r.Method == http.MethodHead:
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodGet && query.Get("list-type") == "2":
		w.Header().Set("Content-Type", "application/xml")
		io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Name>backups</Name>
  <Prefix>mcpd/</Prefix>
  <KeyCount>3</KeyCount>
  <MaxKeys>1000</MaxKeys>
  <IsTruncated>false</IsTruncated>
  <Contents>
    <Key>mcpd/mcpd-20260104T040000Z.mcpdbak</Key>
    <Size>1000</Size>
    <LastModified>2026-01-04T04:00:00.000Z</LastModified>
  </Contents>
  <Contents>
    <Key>mcpd/mcpd-20260105T050000Z.mcpdbak</Key>
    <Size>2000</Size>
    <LastModified>2026-01-05T05:00:00.000Z</LastModified>
  </Contents>
  <Contents>
    <Key>mcpd/notes.txt</Key>
    <Size>500</Size>
    <LastModified>2026-01-06T06:00:00.000Z</LastModified>
  </Contents>
</ListBucketResult>`)
	case r.Method == http.MethodGet && query.Has("location"):
		w.Header().Set("Content-Type", "application/xml")
		io.WriteString(w, `<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>`)
	case r.Method == http.MethodPut:
		w.Header().Set("ETag", `"d41d8cd98f00b204e9800998ecf8427e"`)
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodDelete:
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `<Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message></Error>`)
	}
}

func (s *fakeS3Server) recordedRequests() []s3Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]s3Request(nil), s.requests...)
}

func openTestS3Destination(t *testing.T) (Transport, *fakeS3Server) {
	t.Helper()
	fake := &fakeS3Server{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	transport, err := OpenDestination(Destination{
		Kind: KindS3,
		Settings: Settings{
			Endpoint:      strings.TrimPrefix(srv.URL, "http://"),
			Bucket:        "backups",
			Prefix:        "mcpd/",
			AccessKey:     "ak",
			PathStyle:     true,
			AllowInsecure: true,
		},
		Secret: "sk",
	}, TransportOptions{})
	if err != nil {
		t.Fatalf("OpenDestination: %v", err)
	}
	t.Cleanup(func() {
		if err := transport.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return transport, fake
}

// An upload addresses the bucket and the folder inside it that were configured,
// and keeps the archive's own name -- which is what retention reads the date out
// of on every run afterwards.
func TestS3PutSendsTheArchiveUnderThePrefix(t *testing.T) {
	transport, fake := openTestS3Destination(t)
	const name = "mcpd-20260104T040000Z.mcpdbak"
	const contents = "test archive content"
	body := strings.NewReader(contents)
	if err := transport.Put(t.Context(), name, body, int64(body.Len())); err != nil {
		t.Fatalf("Put: %v", err)
	}

	puts := 0
	for _, request := range fake.recordedRequests() {
		if request.method == http.MethodPut {
			puts++
			if want := "/backups/mcpd/" + name; request.path != want {
				t.Errorf("PUT path = %q, want %q", request.path, want)
			}
			if request.bytes != int64(len(contents)) {
				t.Errorf("PUT carried %d bytes, want %d", request.bytes, len(contents))
			}
		}
	}
	if puts != 1 {
		t.Errorf("sent %d PUT requests, want 1", puts)
	}
}

// A listing carries only mcpd's own archives, named without the folder in front.
//
// A bucket is very often shared, and retention reads this listing: a file
// somebody else put there must be invisible from here rather than something
// mcpd decides whether to keep.
func TestS3ListReturnsOnlyOurOwnArchives(t *testing.T) {
	transport, fake := openTestS3Destination(t)
	objects, err := transport.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := map[string]Object{
		"mcpd-20260104T040000Z.mcpdbak": {
			Size: 1000, ModTime: time.Date(2026, time.January, 4, 4, 0, 0, 0, time.UTC),
		},
		"mcpd-20260105T050000Z.mcpdbak": {
			Size: 2000, ModTime: time.Date(2026, time.January, 5, 5, 0, 0, 0, time.UTC),
		},
	}
	if len(objects) != len(want) {
		t.Fatalf("List returned %d objects, want %d: %+v", len(objects), len(want), objects)
	}
	for _, object := range objects {
		expected, ok := want[object.Name]
		if !ok {
			t.Errorf("List returned unexpected or duplicate object %q", object.Name)
			continue
		}
		if object.Size != expected.Size || !object.ModTime.Equal(expected.ModTime) {
			t.Errorf("metadata for %q = (%d, %s), want (%d, %s)",
				object.Name, object.Size, object.ModTime, expected.Size, expected.ModTime)
		}
		delete(want, object.Name)
	}
	for name := range want {
		t.Errorf("List omitted %q", name)
	}

	lists := 0
	for _, request := range fake.recordedRequests() {
		if request.method == http.MethodGet && request.query.Get("list-type") == "2" {
			lists++
			if request.path != "/backups/" || request.query.Get("prefix") != "mcpd/" {
				t.Errorf("LIST addressed unexpected bucket or prefix: %+v", request)
			}
		}
	}
	if lists != 1 {
		t.Errorf("sent %d LIST requests, want 1", lists)
	}
}

// Delete only ever takes a name this package wrote, and refuses before the
// request is built rather than after.
func TestS3DeleteRefusesAnythingThatIsNotOurs(t *testing.T) {
	transport, fake := openTestS3Destination(t)
	if err := transport.Delete(t.Context(), "notes.txt"); err == nil {
		t.Fatal("Delete(notes.txt) succeeded, want an error")
	}
	for _, request := range fake.recordedRequests() {
		if request.method == http.MethodDelete {
			t.Errorf("Delete(notes.txt) sent a DELETE request: %+v", request)
		}
	}
}

// Test connection writes as well as reads.
//
// A credential with ListBucket and no PutObject lists happily and fails at four
// in the morning. The probe is what finds that now. Its name is deliberately not
// one TimeFromName parses, so a probe left by an interrupted check can never be
// counted as a backup or deleted as one.
func TestS3CheckWritesAndRemovesAProbe(t *testing.T) {
	transport, fake := openTestS3Destination(t)
	if err := transport.Check(t.Context()); err != nil {
		t.Fatalf("Check: %v", err)
	}

	listed := false
	var mutations []s3Request
	for _, request := range fake.recordedRequests() {
		if request.method == http.MethodGet && request.query.Get("list-type") == "2" {
			listed = true
		}
		if request.method == http.MethodPut || request.method == http.MethodDelete {
			if !listed {
				t.Errorf("Check sent %s before listing the bucket", request.method)
			}
			mutations = append(mutations, request)
		}
	}
	if !listed {
		t.Error("Check did not list the bucket")
	}
	if len(mutations) != 2 {
		t.Fatalf("Check mutations = %+v, want one PUT and one DELETE", mutations)
	}
	for i, method := range []string{http.MethodPut, http.MethodDelete} {
		request := mutations[i]
		if request.method != method || request.path != "/backups/mcpd/.mcpd-check" {
			t.Errorf("Check mutation %d = %+v, want %s /backups/mcpd/.mcpd-check", i, request, method)
		}
		if _, ours := TimeFromName(path.Base(request.path)); ours {
			t.Errorf("probe %q parses as an archive", request.path)
		}
	}
}
