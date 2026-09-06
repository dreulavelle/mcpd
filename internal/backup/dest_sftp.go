package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// SFTP, which is the answer for a Synology NAS and for most of the boxes people
// already own.
//
// The interesting part is not the transfer, it is the identity. An SFTP server
// is trusted on its host key and nothing else, so a client that accepts
// whatever answers is a client that will hand a whole instance's credentials to
// anything sitting on that address. mcpd therefore refuses to run against a
// destination with no key recorded, records one only when somebody presses Test
// connection, and treats a change as a hard refusal rather than something to
// re-learn.

// dialTimeout bounds reaching a server that is off or firewalled. Short enough
// that a scheduled run does not sit on it, long enough for a NAS waking a disk.
const dialTimeout = 30 * time.Second

type sftpTransport struct {
	dir    string
	addr   string
	config *ssh.ClientConfig

	// presented is what the server showed, recorded by the host key callback so
	// Test connection can report it. Guarded because the callback runs on the
	// handshake's goroutine.
	mu        sync.Mutex
	presented string

	client *sftp.Client
	ssh    *ssh.Client
}

// HostKeyMismatch reports a server presenting an identity other than the one
// recorded against the destination.
//
// A distinct type rather than a wrapped string because it is the one failure
// here that must never be retried, re-learned, or reported as "the server is
// having trouble".
type HostKeyMismatch struct {
	Host string
	Want string
	Got  string
}

func (e *HostKeyMismatch) Error() string {
	return "The server presented a different host key from the one recorded, " +
		"so mcpd did not send the backup. If the server was rebuilt or its keys " +
		"were replaced, clear the recorded key on the destination and test the " +
		"connection again."
}

// Evidence keeps the two fingerprints out of the sentence and under Technical
// details, where a person can compare them with what the server says itself.
func (e *HostKeyMismatch) Evidence() string {
	return fmt.Sprintf("%s presented %s; %s was recorded", e.Host, e.Got, e.Want)
}

func openSFTP(d Destination, opts TransportOptions) (Transport, error) {
	pinned := strings.TrimSpace(d.HostKey)
	if pinned == "" && !opts.LearnHostKey {
		return nil, ErrNoHostKey
	}

	auth, err := sftpAuth(d)
	if err != nil {
		return nil, err
	}
	port := d.Settings.Port
	if port == 0 {
		port = 22
	}
	addr := net.JoinHostPort(d.Settings.Host, strconv.Itoa(port))

	t := &sftpTransport{
		dir:  sftpDir(d.Settings.RemotePath),
		addr: addr,
	}
	t.config = &ssh.ClientConfig{
		User:            d.Settings.Username,
		Auth:            auth,
		Timeout:         dialTimeout,
		HostKeyCallback: t.checkHostKey(pinned, d.Settings.Host),
	}
	return t, nil
}

// checkHostKey is the whole of the trust decision.
func (t *sftpTransport) checkHostKey(pinned, host string) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		got := ssh.FingerprintSHA256(key)
		t.mu.Lock()
		t.presented = got
		t.mu.Unlock()

		if pinned == "" {
			// Only reachable with LearnHostKey, which only the Test connection
			// endpoint sets. openSFTP refuses an empty pin otherwise.
			return nil
		}
		if got != pinned {
			return &HostKeyMismatch{Host: host, Want: pinned, Got: got}
		}
		return nil
	}
}

// PresentedHostKey reports what the server showed, for the endpoint that
// records it.
func (t *sftpTransport) PresentedHostKey() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.presented
}

func sftpAuth(d Destination) ([]ssh.AuthMethod, error) {
	secret := d.Secret
	if strings.TrimSpace(secret) == "" {
		return nil, errors.New("backup: this destination has no password or key to sign in with")
	}
	if !d.Settings.KeyAuth {
		return []ssh.AuthMethod{ssh.Password(secret)}, nil
	}
	signer, err := ssh.ParsePrivateKey([]byte(secret))
	if err != nil {
		// The common cases are a key with a passphrase on it and a public key
		// pasted where the private one belongs, and neither is worth making
		// somebody read an ASN.1 error to work out.
		return nil, errors.New("backup destination: that private key could not be " +
			"read. It must be an unencrypted OpenSSH or PEM private key -- the " +
			"file without .pub on the end, with no passphrase")
	}
	return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
}

// sftpDir settles the remote directory. Empty means the login directory, which
// is what an operator who left the box blank meant.
func sftpDir(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "."
	}
	return path.Clean(p)
}

// connect opens the session lazily, so that building a transport does no I/O
// and a caller can decide what to do with a refusal before anything is dialled.
func (t *sftpTransport) connect(ctx context.Context) (*sftp.Client, error) {
	if t.client != nil {
		return t.client, nil
	}
	dialer := net.Dialer{Timeout: dialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", t.addr)
	if err != nil {
		return nil, fmt.Errorf("reach %s: %w", t.addr, err)
	}
	// The handshake gets the same budget as the dial did; without a deadline a
	// server that accepts the connection and then says nothing holds a run
	// open until the process stops.
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(dialTimeout))
	}

	c, chans, reqs, err := ssh.NewClientConn(conn, t.addr, t.config)
	if err != nil {
		conn.Close()
		// Unwrapped, so a mismatch stays the type the runner reports as a
		// refusal rather than as a server having trouble.
		var mismatch *HostKeyMismatch
		if errors.As(err, &mismatch) {
			return nil, mismatch
		}
		return nil, fmt.Errorf("sign in to %s: %w", t.addr, err)
	}
	_ = conn.SetDeadline(time.Time{})

	t.ssh = ssh.NewClient(c, chans, reqs)
	client, err := sftp.NewClient(t.ssh)
	if err != nil {
		t.ssh.Close()
		t.ssh = nil
		return nil, fmt.Errorf("start SFTP on %s: %w", t.addr, err)
	}
	t.client = client
	return client, nil
}

func (t *sftpTransport) Put(ctx context.Context, name string, r io.Reader, _ int64) error {
	client, err := t.connect(ctx)
	if err != nil {
		return err
	}
	final := path.Join(t.dir, name)
	// A partial upload must not wear the final name: retention counts by name,
	// and a person restoring picks by name.
	staging := final + ".part"

	f, err := client.Create(staging)
	if err != nil {
		return fmt.Errorf("create %s: %w", staging, err)
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		_ = client.Remove(staging)
		return fmt.Errorf("write %s: %w", staging, err)
	}
	if err := f.Close(); err != nil {
		_ = client.Remove(staging)
		return fmt.Errorf("close %s: %w", staging, err)
	}
	if err := client.Rename(staging, final); err != nil {
		_ = client.Remove(staging)
		return fmt.Errorf("rename %s: %w", final, err)
	}
	return nil
}

func (t *sftpTransport) List(ctx context.Context) ([]Object, error) {
	client, err := t.connect(ctx)
	if err != nil {
		return nil, err
	}
	entries, err := client.ReadDir(t.dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", t.dir, err)
	}
	var out []Object
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if _, ours := TimeFromName(e.Name()); !ours {
			continue
		}
		out = append(out, Object{Name: e.Name(), Size: e.Size(), ModTime: e.ModTime()})
	}
	return out, nil
}

func (t *sftpTransport) Delete(ctx context.Context, name string) error {
	if _, ours := TimeFromName(name); !ours {
		return fmt.Errorf("backup: %q is not an archive mcpd wrote", name)
	}
	client, err := t.connect(ctx)
	if err != nil {
		return err
	}
	if err := client.Remove(path.Join(t.dir, name)); err != nil {
		return fmt.Errorf("remove %s: %w", name, err)
	}
	return nil
}

func (t *sftpTransport) Check(ctx context.Context) error {
	client, err := t.connect(ctx)
	if err != nil {
		return err
	}
	if _, err := client.ReadDir(t.dir); err != nil {
		return fmt.Errorf("read %s: %w", t.dir, err)
	}
	// A user with read on the share and no write is the ordinary Synology
	// mistake, and listing alone does not find it.
	probe := path.Join(t.dir, ".mcpd-check")
	f, err := client.Create(probe)
	if err != nil {
		return fmt.Errorf("write to %s: %w", t.dir, err)
	}
	f.Close()
	if err := client.Remove(probe); err != nil {
		return fmt.Errorf("remove %s: %w", probe, err)
	}
	return nil
}

func (t *sftpTransport) Close() error {
	var errs []error
	if t.client != nil {
		errs = append(errs, t.client.Close())
		t.client = nil
	}
	if t.ssh != nil {
		errs = append(errs, t.ssh.Close())
		t.ssh = nil
	}
	return errors.Join(errs...)
}
