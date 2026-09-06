package backup

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"time"
)

// Where an archive goes when nobody is watching.
//
// A destination is a row rather than a setting because there can be several,
// each with a credential and a retention of its own, and because the thing an
// operator does with one is add it, test it, and read what happened last time.
//
// The transport is the narrow half. Four kinds implement it -- a directory on
// this host, SFTP, an S3-compatible bucket, WebDAV -- and everything above
// them, the runner and the API alike, only ever puts a file, lists what is
// there, and removes what retention says to remove.

// Kind is which transport a destination speaks.
type Kind string

const (
	// KindLocal is a directory on this host. It covers an SMB or NFS share
	// mounted by the operating system, and it is where an rclone or rsync
	// pipeline points.
	KindLocal Kind = "local"
	// KindSFTP is the universal answer, and the one a Synology NAS gives.
	KindSFTP Kind = "sftp"
	// KindS3 is anything speaking S3: AWS, Cloudflare R2, Backblaze B2,
	// Wasabi, MinIO, Hetzner, DigitalOcean Spaces.
	KindS3 Kind = "s3"
	// KindWebDAV is Synology's WebDAV Server, Nextcloud, a Hetzner Storage
	// Box.
	KindWebDAV Kind = "webdav"
)

// Kinds are the transports this build has, in the order a form should offer
// them: the two most people will pick first.
var Kinds = []Kind{KindLocal, KindSFTP, KindS3, KindWebDAV}

// Valid reports whether k is one of them.
func (k Kind) Valid() bool {
	for _, known := range Kinds {
		if k == known {
			return true
		}
	}
	return false
}

// Settings are a destination's non-secret fields.
//
// One struct for every kind rather than four. A destination is edited in one
// form and stored in one column, and four types would mean four decoders and a
// type switch in everything that only wants to know which host a failure was
// about.
type Settings struct {
	// Path is the directory a local destination writes into.
	Path string `json:"path,omitempty"`

	// Host, Port, Username and RemotePath address an SFTP server.
	Host       string `json:"host,omitempty"`
	Port       int    `json:"port,omitempty"`
	Username   string `json:"username,omitempty"`
	RemotePath string `json:"remote_path,omitempty"`
	// KeyAuth says the secret is a private key in PEM form rather than a
	// password. The two are stored in one column because a destination has one
	// credential; this is what says how to read it.
	KeyAuth bool `json:"key_auth,omitempty"`

	// Endpoint, Region, Bucket, Prefix, AccessKey, PathStyle and Insecure
	// address an S3-compatible service.
	Endpoint  string `json:"endpoint,omitempty"`
	Region    string `json:"region,omitempty"`
	Bucket    string `json:"bucket,omitempty"`
	Prefix    string `json:"prefix,omitempty"`
	AccessKey string `json:"access_key,omitempty"`
	// PathStyle addresses a bucket as endpoint/bucket rather than
	// bucket.endpoint. MinIO and Backblaze B2 want it; AWS does not.
	PathStyle bool `json:"path_style,omitempty"`

	// URL addresses a WebDAV collection.
	URL string `json:"url,omitempty"`

	// AllowInsecure permits plain HTTP for the two kinds that speak it. Off
	// by default and never implied: the archive is encrypted, but the
	// credential authenticating the upload is not, and it crosses the network
	// on every run.
	AllowInsecure bool `json:"allow_insecure,omitempty"`
}

// Policy is how much history one destination keeps.
//
// Four rules, OR-ed: the newest N whatever their dates, plus the newest in
// each of the last so many days, weeks and months. Zero switches a rule off,
// and KeepLast is never zero -- a policy that keeps nothing is a destination
// that deletes the backup it has just written.
type Policy struct {
	KeepLast    int `json:"keep_last"`
	KeepDaily   int `json:"keep_daily"`
	KeepWeekly  int `json:"keep_weekly"`
	KeepMonthly int `json:"keep_monthly"`
}

// DefaultPolicy is what a new destination gets: the last six, and nothing
// clever. Six weekly backups is a month and a half, which is longer than it
// usually takes to notice something is wrong.
var DefaultPolicy = Policy{KeepLast: 6}

// Destination is one stored place to send archives.
type Destination struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind Kind   `json:"kind"`

	Settings Settings `json:"settings"`
	// Secret is the destination's one credential, in the clear. It is filled
	// by the store on the way out and is never rendered: the API's own view
	// type has no field for it.
	Secret string `json:"-"`

	Enabled bool   `json:"enabled"`
	Policy  Policy `json:"policy"`
	// HostKey is the SFTP server's public key as ssh-keygen prints it. Empty
	// means none has been recorded, which for SFTP means the destination
	// cannot be enabled.
	HostKey string `json:"host_key,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// LastRunAt and LastOK describe the last attempt. A nil LastOK is "never
	// ran", which is a different fact from "ran and failed".
	LastRunAt  *time.Time `json:"last_run_at,omitempty"`
	LastOK     *bool      `json:"last_ok,omitempty"`
	LastError  string     `json:"last_error,omitempty"`
	LastDetail string     `json:"last_detail,omitempty"`
	// LastSeen is how many of mcpd's own archives were listed here on the last
	// successful run, which is what makes a listing that has lost most of them
	// recognisable as an answer to distrust.
	LastSeen int `json:"last_seen"`
}

// Where is a short description of where this destination points, carrying no
// credential.
//
// One line for a person: the trail records it beside a destination being added
// or changed, and the dashboard lists it under the name. It is the settings, not
// the secret -- an S3 access key identifies as much as it authenticates, so it
// is not in here either.
func (d Destination) Where() string {
	switch d.Kind {
	case KindLocal:
		return d.Settings.Path
	case KindSFTP:
		at := d.Settings.Host
		if d.Settings.Port != 0 && d.Settings.Port != 22 {
			at = fmt.Sprintf("%s:%d", at, d.Settings.Port)
		}
		if p := strings.TrimSpace(d.Settings.RemotePath); p != "" {
			return at + ":" + p
		}
		return at
	case KindS3:
		where := d.Settings.Endpoint + "/" + d.Settings.Bucket
		if p := strings.Trim(strings.TrimSpace(d.Settings.Prefix), "/"); p != "" {
			where += "/" + p
		}
		return where
	case KindWebDAV:
		return d.Settings.URL
	}
	return ""
}

// DestinationUpdate edits a destination, leaving unset fields alone.
//
// Pointers on everything, so "not sent" and "set to empty" stay different
// instructions. It matters most for Secret: the page never reads a credential
// back, so an edit that changes only the retention arrives with no secret at
// all, and a plain string would read that as an erasure.
type DestinationUpdate struct {
	Name     *string
	Settings *Settings
	Secret   *string
	Enabled  *bool
	Policy   *Policy
	HostKey  *string
}

// Object is one file a destination holds.
type Object struct {
	Name    string
	Size    int64
	ModTime time.Time
}

// Transport is what a destination can be asked to do.
//
// Deliberately four verbs. Everything above this -- the runner, retention, the
// Test connection button -- is written against these, so adding a fifth kind
// of destination is one file and no changes anywhere else.
type Transport interface {
	// Put writes an archive under name. size is known because the archive is
	// spooled to disk first, and several of these transports need a length
	// before they will accept a body.
	Put(ctx context.Context, name string, r io.Reader, size int64) error
	// List returns only mcpd's own archives. A destination is very often a
	// shared folder, and retention must never be able to consider a file
	// somebody else put there.
	List(ctx context.Context) ([]Object, error)
	// Delete removes one archive by name.
	Delete(ctx context.Context, name string) error
	// Check answers the Test connection button: reach the destination, list
	// it, and write and remove a small probe, so that a credential which can
	// read but not write is found now rather than at four in the morning.
	Check(ctx context.Context) error
	// Close releases whatever the transport holds open.
	Close() error
}

// TransportOptions are what opening a transport needs from the host.
type TransportOptions struct {
	// Pool is the roots this host trusts, from internal/trust, so a NAS or a
	// MinIO behind a company authority is reachable without anybody being told
	// to switch TLS verification off. Nil uses the system roots.
	Pool *x509.CertPool
	// LearnHostKey lets an SFTP transport accept whatever key the server
	// presents and report it afterwards, instead of refusing.
	//
	// Set by the Test connection endpoint and by nothing else. A run that
	// learned an identity would be trusting whatever answered on the night,
	// which is the whole thing pinning a host key exists to prevent.
	LearnHostKey bool
	Log          *slog.Logger
	Now          func() time.Time
}

func (o TransportOptions) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// HostKeyReporter is implemented by a transport that pins a server's identity.
// It reports what the server actually presented, so the Test connection
// endpoint can record it and show it to the operator.
type HostKeyReporter interface {
	PresentedHostKey() string
}

// ErrNoHostKey reports an SFTP destination with nothing recorded to check the
// server against.
var ErrNoHostKey = errors.New(
	"backup: this destination has no recorded host key, so mcpd cannot tell " +
		"the server apart from anything else answering at that address. Press " +
		"Test connection to record the key it presents, or paste the " +
		"fingerprint from the server itself")

// OpenDestination builds the transport for a destination. It does no I/O: what
// it can refuse -- a kind this build does not have, an SFTP destination with no
// recorded host key -- is refused before anything is dialled.
func OpenDestination(d Destination, opts TransportOptions) (Transport, error) {
	switch d.Kind {
	case KindLocal:
		return openLocal(d, opts)
	case KindSFTP:
		return openSFTP(d, opts)
	case KindS3:
		return openS3(d, opts)
	case KindWebDAV:
		return openWebDAV(d, opts)
	default:
		return nil, fmt.Errorf("backup: %q is not a kind of destination this build has", d.Kind)
	}
}

// Validate checks a destination's own fields before it is stored.
//
// Here rather than in the handler, so the same rules apply to a value arriving
// from the dashboard, from the API, and from a future import.
func (d *Destination) Validate(storageDir string) error {
	d.Name = strings.TrimSpace(d.Name)
	if d.Name == "" {
		return errors.New("backup destination: give it a name")
	}
	if !d.Kind.Valid() {
		return fmt.Errorf("backup destination: %q is not a kind of destination this build has", d.Kind)
	}
	if d.Policy.KeepLast < 1 {
		return errors.New("backup destination: keep at least one archive; " +
			"a destination that keeps none deletes the backup it has just written")
	}
	if d.Policy.KeepDaily < 0 || d.Policy.KeepWeekly < 0 || d.Policy.KeepMonthly < 0 {
		return errors.New("backup destination: a retention count cannot be negative")
	}

	switch d.Kind {
	case KindLocal:
		resolved, err := resolveLocalPath(d.Settings.Path, storageDir)
		if err != nil {
			return err
		}
		d.Settings.Path = resolved

	case KindSFTP:
		if strings.TrimSpace(d.Settings.Host) == "" {
			return errors.New("backup destination: give the server's address")
		}
		if strings.TrimSpace(d.Settings.Username) == "" {
			return errors.New("backup destination: give the user mcpd signs in as")
		}
		if d.Settings.Port == 0 {
			d.Settings.Port = 22
		}
		if d.Settings.Port < 1 || d.Settings.Port > 65535 {
			return errors.New("backup destination: that is not a port number")
		}
		if d.Enabled && strings.TrimSpace(d.HostKey) == "" {
			return ErrNoHostKey
		}
		if hk := strings.TrimSpace(d.HostKey); hk != "" && !strings.HasPrefix(hk, "SHA256:") {
			return errors.New("backup destination: the host key should be the " +
				"SHA256: fingerprint ssh-keygen prints, not the key itself")
		}

	case KindS3:
		if strings.TrimSpace(d.Settings.Bucket) == "" {
			return errors.New("backup destination: give the bucket")
		}
		if strings.TrimSpace(d.Settings.Endpoint) == "" {
			return errors.New("backup destination: give the service's address")
		}
		if strings.Contains(d.Settings.Endpoint, "://") {
			return errors.New("backup destination: the address is a host, " +
				"without http:// or https:// in front of it")
		}
		if d.Settings.AllowInsecure && !isLoopback(d.Settings.Endpoint) {
			// Refused rather than warned about. An S3 secret key signs every
			// request and is worth as much as the bucket.
			return errors.New("backup destination: mcpd will only talk to a " +
				"bucket over plain HTTP when the service is on this machine")
		}

	case KindWebDAV:
		u, err := url.Parse(strings.TrimSpace(d.Settings.URL))
		if err != nil || u.Host == "" {
			return errors.New("backup destination: give the full address of the " +
				"folder, like https://nas.example.com/backups/mcpd")
		}
		if u.User != nil {
			// A credential in the address ends up in a log line, a copied
			// link, and this row's own configuration column.
			return errors.New("backup destination: put the user name and " +
				"password in their own boxes, not in the address")
		}
		switch u.Scheme {
		case "https":
		case "http":
			if !d.Settings.AllowInsecure {
				return errors.New("backup destination: that address is not " +
					"encrypted. The archive is, but the password is not, and it " +
					"crosses the network on every run. Use https, or tick the box " +
					"to send it in the clear anyway")
			}
		default:
			return errors.New("backup destination: the address must start with https://")
		}
		d.Settings.URL = strings.TrimSuffix(u.String(), "/")
	}
	return nil
}

// isLoopback reports whether an S3 endpoint names this machine, which is the
// one case where plain HTTP carries a secret key no further than the host.
func isLoopback(endpoint string) bool {
	host := endpoint
	if i := strings.LastIndex(host, ":"); i > 0 && !strings.Contains(host[i:], "]") {
		host = host[:i]
	}
	host = strings.Trim(host, "[]")
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
