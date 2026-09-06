package backup

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// The SFTP tests run a real SSH server in this process, serving a real SFTP
// subsystem over a loopback listener, because the thing being defended is the
// handshake rather than the file transfer -- and a fake that answered the
// handshake would be testing the fake.

// sshServer is an in-process SSH server with pkg/sftp's server as its
// subsystem, rooted at a temporary directory.
type sshServer struct {
	host        string
	port        int
	dir         string
	fingerprint string
}

// startSSHServer listens on loopback and serves SFTP until the test ends.
func startSSHServer(t *testing.T) *sshServer {
	t.Helper()

	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate a host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatalf("host key signer: %v", err)
	}

	config := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if string(password) != "hunter2" {
				return nil, errors.New("no")
			}
			return nil, nil
		},
		PublicKeyCallback: func(_ ssh.ConnMetadata, _ ssh.PublicKey) (*ssh.Permissions, error) {
			// Any key: what these tests exercise is the *host* key, not the
			// client's, and refusing here would only test this callback.
			return nil, nil
		},
	}
	config.AddHostKey(signer)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { listener.Close() })

	addr := listener.Addr().(*net.TCPAddr)
	srv := &sshServer{
		host:        "127.0.0.1",
		port:        addr.Port,
		dir:         t.TempDir(),
		fingerprint: ssh.FingerprintSHA256(signer.PublicKey()),
	}

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go serveSFTP(conn, config)
		}
	}()
	return srv
}

// serveSFTP completes one handshake and runs the subsystem. Errors are dropped
// rather than reported: a test that refuses a host key closes the connection
// mid-handshake on purpose, and the server saying so is noise.
func serveSFTP(conn net.Conn, config *ssh.ServerConfig) {
	defer conn.Close()
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	defer sshConn.Close()
	go ssh.DiscardRequests(reqs)

	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			newChannel.Reject(ssh.UnknownChannelType, "only sessions here")
			continue
		}
		channel, requests, err := newChannel.Accept()
		if err != nil {
			return
		}
		go func(in <-chan *ssh.Request) {
			for req := range in {
				ok := req.Type == "subsystem" &&
					len(req.Payload) >= 4 && string(req.Payload[4:]) == "sftp"
				req.Reply(ok, nil)
			}
		}(requests)

		server, err := sftp.NewServer(channel)
		if err != nil {
			channel.Close()
			continue
		}
		go func() {
			defer server.Close()
			_ = server.Serve()
		}()
	}
}

// destination builds a destination pointing at this server.
func (s *sshServer) destination(hostKey string) Destination {
	return Destination{
		ID: "dst_test", Name: "nas", Kind: KindSFTP,
		Settings: Settings{
			Host: s.host, Port: s.port, Username: "ops", RemotePath: s.dir,
		},
		Secret:  "hunter2",
		HostKey: hostKey,
		Policy:  DefaultPolicy,
	}
}

// A host key that has changed is a hard refusal, never something to re-learn.
//
// This is the whole reason a host key is stored at all. Anything that can put
// itself on that address gets a complete copy of this instance -- every
// account, every credential, the whole audit trail -- if mcpd will talk to it.
func TestSFTPRefusesAChangedHostKey(t *testing.T) {
	first := startSSHServer(t)
	second := startSSHServer(t)
	if first.fingerprint == second.fingerprint {
		t.Fatal("two servers generated the same host key")
	}

	// Pinned to the first server, pointed at the second.
	d := second.destination(first.fingerprint)
	transport, err := OpenDestination(d, TransportOptions{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer transport.Close()

	err = transport.Check(t.Context())
	if err == nil {
		t.Fatal("mcpd talked to a server presenting a key it had not seen before")
	}

	var mismatch *HostKeyMismatch
	if !errors.As(err, &mismatch) {
		t.Fatalf("got %v, want a host key mismatch that nothing retries", err)
	}
	if mismatch.Got != second.fingerprint || mismatch.Want != first.fingerprint {
		t.Errorf("mismatch names %s presented and %s recorded, want %s and %s",
			mismatch.Got, mismatch.Want, second.fingerprint, first.fingerprint)
	}

	// The sentence carries no fingerprints, and the evidence carries both. A
	// person reads the first and compares the second with what the server says
	// about itself.
	if strings.Contains(mismatch.Error(), "SHA256:") {
		t.Errorf("the sentence carries evidence: %q", mismatch.Error())
	}
	if !strings.Contains(mismatch.Evidence(), second.fingerprint) ||
		!strings.Contains(mismatch.Evidence(), first.fingerprint) {
		t.Errorf("the evidence does not carry both fingerprints: %q", mismatch.Evidence())
	}
}

// No trust on first use during a run. A destination with nothing recorded is
// refused before anything is dialled, so a scheduled backup can never learn an
// identity from whatever happens to answer that night.
func TestSFTPRefusesADestinationWithNoRecordedHostKey(t *testing.T) {
	srv := startSSHServer(t)
	_, err := OpenDestination(srv.destination(""), TransportOptions{})
	if !errors.Is(err, ErrNoHostKey) {
		t.Fatalf("got %v, want a refusal naming the missing host key", err)
	}
}

// Test connection is the one path that records a key, and it reports what it
// saw so an operator can compare it with `ssh-keyscan` before enabling the
// destination.
func TestSFTPLearnsTheHostKeyOnlyWhenAskedTo(t *testing.T) {
	srv := startSSHServer(t)
	transport, err := OpenDestination(srv.destination(""), TransportOptions{LearnHostKey: true})
	if err != nil {
		t.Fatalf("open with learning on: %v", err)
	}
	defer transport.Close()

	if err := transport.Check(t.Context()); err != nil {
		t.Fatalf("check: %v", err)
	}
	reporter, ok := transport.(HostKeyReporter)
	if !ok {
		t.Fatal("the SFTP transport does not report what the server presented")
	}
	if got := reporter.PresentedHostKey(); got != srv.fingerprint {
		t.Errorf("presented %q, want %q", got, srv.fingerprint)
	}
	// The format is what ssh-keygen prints, so the documentation can tell
	// somebody to compare the two strings.
	if !strings.HasPrefix(reporter.PresentedHostKey(), "SHA256:") {
		t.Errorf("fingerprint %q is not in ssh-keygen's format", reporter.PresentedHostKey())
	}
}

func TestSFTPRoundTrip(t *testing.T) {
	srv := startSSHServer(t)
	transport, err := OpenDestination(srv.destination(srv.fingerprint), TransportOptions{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer transport.Close()
	ctx := t.Context()

	const name = "mcpd-nas-20260208T040000Z.mcpdbak"
	body := "an archive, pretend"
	if err := transport.Put(ctx, name, strings.NewReader(body), int64(len(body))); err != nil {
		t.Fatalf("put: %v", err)
	}

	// The final name and no partial beside it: an upload that stopped halfway
	// must not leave a short file that retention counts and a person restores.
	entries, err := os.ReadDir(srv.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != name {
		got := make([]string, 0, len(entries))
		for _, e := range entries {
			got = append(got, e.Name())
		}
		t.Fatalf("the server holds %v, want just %s", got, name)
	}
	stored, err := os.ReadFile(filepath.Join(srv.dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != body {
		t.Errorf("the server holds %q", stored)
	}

	// Somebody else's file in a shared folder is invisible.
	if err := os.WriteFile(filepath.Join(srv.dir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	objects, err := transport.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(objects) != 1 || objects[0].Name != name {
		t.Fatalf("list returned %+v, want only mcpd's own archive", objects)
	}

	if err := transport.Delete(ctx, "notes.txt"); err == nil {
		t.Error("delete accepted a name mcpd did not write")
	}
	if err := transport.Delete(ctx, name); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(srv.dir, name)); !os.IsNotExist(err) {
		t.Errorf("the archive is still there: %v", err)
	}
}

// A private key is stored in the same column a password is, and KeyAuth is what
// says which. A public key pasted where the private one belongs is the common
// mistake, and it gets a sentence rather than an ASN.1 error.
func TestSFTPExplainsAKeyItCannotRead(t *testing.T) {
	srv := startSSHServer(t)
	d := srv.destination(srv.fingerprint)
	d.Settings.KeyAuth = true
	d.Secret = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIexample ops@laptop"

	_, err := OpenDestination(d, TransportOptions{})
	if err == nil {
		t.Fatal("a public key was accepted as a private one")
	}
	if !strings.Contains(err.Error(), "without .pub") {
		t.Errorf("the refusal does not say what to paste instead: %v", err)
	}
}

// A cancelled context stops an operation now rather than at its deadline.
//
// pkg/sftp's calls take no context and block on a reply that may never come, so
// the socket is the only thing that can end one. Without this, a shutdown or an
// abandoned Test connection waits out the whole budget, and the single worker
// waits with it.
func TestSFTPStopsWhenTheCallerGivesUp(t *testing.T) {
	srv := startSSHServer(t)
	transport, err := OpenDestination(srv.destination(srv.fingerprint), TransportOptions{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer transport.Close()

	// Connected first, so what is being tested is an operation on a live
	// session rather than a dial that never happened.
	if err := transport.Check(t.Context()); err != nil {
		t.Fatalf("check: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	done := make(chan error, 1)
	go func() { _, err := transport.List(ctx); done <- err }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a listing with a cancelled context came back successfully")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("a listing with a cancelled context did not return; the 60-second " +
			"operation budget is not what should have ended it")
	}
}

// A dial with a cancelled context fails before anything is sent.
func TestSFTPRefusesToDialForACallerThatHasGivenUp(t *testing.T) {
	srv := startSSHServer(t)
	transport, err := OpenDestination(srv.destination(srv.fingerprint), TransportOptions{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer transport.Close()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := transport.List(ctx); err == nil {
		t.Error("a listing with a cancelled context came back successfully")
	}
}

// An upload's budget grows with the archive, because an archive is the one
// thing here whose size nobody knows in advance.
//
// A fixed minute would fail every real backup; a fixed day would be no bound at
// all on a listing. The rate is deliberately slower than any link somebody
// would put a NAS on: this is a ceiling on being stuck, not a target.
func TestTheUploadBudgetGrowsWithTheArchive(t *testing.T) {
	small := uploadTimeout(1 << 20) // 1 MiB
	if small < uploadBase || small > uploadBase+time.Minute {
		t.Errorf("a 1 MiB archive gets %s, want about the %s base", small, uploadBase)
	}
	large := uploadTimeout(4 << 30) // 4 GiB
	if large <= uploadBase {
		t.Errorf("a 4 GiB archive gets %s, which is no more than the floor", large)
	}
	if capped := uploadTimeout(1 << 50); capped != 24*time.Hour {
		t.Errorf("an absurd size gets %s, want it capped at a day", capped)
	}
}

// A port nothing is listening on is a failure with the address in it, so
// somebody reading the run history knows where mcpd went.
func TestSFTPReportsAServerItCannotReach(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()

	d := Destination{
		Kind: KindSFTP, Name: "gone",
		Settings: Settings{Host: "127.0.0.1", Port: port, Username: "ops"},
		Secret:   "hunter2", HostKey: "SHA256:whatever",
	}
	transport, err := OpenDestination(d, TransportOptions{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer transport.Close()

	err = transport.Check(t.Context())
	if err == nil {
		t.Fatal("a closed port answered")
	}
	if !strings.Contains(err.Error(), strconv.Itoa(port)) {
		t.Errorf("the failure does not name the address: %v", err)
	}
}
