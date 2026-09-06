package backup

import (
	"context"
	"crypto/tls"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
)

// WebDAV: Synology's WebDAV Server package, Nextcloud, a Hetzner Storage Box.
//
// net/http rather than a WebDAV library. What this needs of the protocol is
// four verbs -- PUT, PROPFIND at depth 1, DELETE, and MKCOL for the folder --
// and one small XML document, and the library that would save writing them
// brings its own opinions about redirects, about what a 207 means, and about
// which errors are worth retrying. A dependency is a thing to keep in step, and
// this one would be kept in step for about ninety lines.

type webdavTransport struct {
	base     *url.URL
	user     string
	password string
	client   *http.Client
}

func openWebDAV(d Destination, opts TransportOptions) (Transport, error) {
	raw := strings.TrimSpace(d.Settings.URL)
	base, err := url.Parse(raw)
	if err != nil || base.Host == "" {
		return nil, fmt.Errorf("backup: %q is not an address this host can reach", raw)
	}
	if base.Scheme == "http" && !d.Settings.AllowInsecure {
		return nil, errors.New("backup: this destination's address is not " +
			"encrypted and it is not marked as allowed")
	}
	// One trailing slash, so joining a name never produces two or none.
	base.Path = strings.TrimSuffix(base.Path, "/") + "/"

	transport := http.DefaultTransport.(*http.Transport).Clone()
	if opts.Pool != nil {
		transport.TLSClientConfig = &tls.Config{RootCAs: opts.Pool, MinVersion: tls.VersionTLS12}
	}

	return &webdavTransport{
		base:     base,
		user:     strings.TrimSpace(d.Settings.Username),
		password: d.Secret,
		client: &http.Client{
			Transport: transport,
			// A redirect would re-send the credential to whatever the server
			// named, which is not something an operator agreed to when they
			// typed one address.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (d *webdavTransport) url(name string) string {
	u := *d.base
	u.Path = path.Join(u.Path, name)
	return u.String()
}

func (d *webdavTransport) request(ctx context.Context, method, target string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	if d.user != "" {
		req.SetBasicAuth(d.user, d.password)
	}
	return req, nil
}

// webdavError turns a response the server refused into something with the
// sentence and the evidence apart.
type webdavError struct {
	method string
	target string
	status int
}

func (e *webdavError) Error() string {
	switch {
	case e.status == http.StatusUnauthorized || e.status == http.StatusForbidden:
		return "The server refused the user name and password."
	case e.status == http.StatusNotFound:
		return "The folder at that address is not there."
	case e.status == http.StatusInsufficientStorage:
		return "The server says it is out of space."
	default:
		return "The server would not accept the backup."
	}
}

func (e *webdavError) Evidence() string {
	return fmt.Sprintf("%s %s answered %d %s",
		e.method, e.target, e.status, http.StatusText(e.status))
}

func (d *webdavTransport) do(req *http.Request, ok ...int) (*http.Response, error) {
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reach %s: %w", req.URL.Host, err)
	}
	for _, want := range ok {
		if resp.StatusCode == want {
			return resp, nil
		}
	}
	resp.Body.Close()
	return nil, &webdavError{method: req.Method, target: req.URL.Redacted(), status: resp.StatusCode}
}

func (d *webdavTransport) Put(ctx context.Context, name string, r io.Reader, size int64) error {
	req, err := d.request(ctx, http.MethodPut, d.url(name), r)
	if err != nil {
		return err
	}
	// Declared rather than chunked. Several WebDAV servers -- Synology's among
	// them -- refuse a chunked body, and a length is known here because the
	// archive was spooled to disk before any of this ran.
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := d.do(req, http.StatusOK, http.StatusCreated, http.StatusNoContent)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// multistatus is the part of a PROPFIND answer this needs. The namespace is
// fixed by the specification, and everything outside these four elements is
// server-specific and ignored.
type multistatus struct {
	XMLName   xml.Name `xml:"DAV: multistatus"`
	Responses []struct {
		Href  string `xml:"href"`
		Props []struct {
			Length       int64  `xml:"prop>getcontentlength"`
			LastModified string `xml:"prop>getlastmodified"`
			Collection   *struct {
			} `xml:"prop>resourcetype>collection"`
		} `xml:"propstat"`
	} `xml:"response"`
}

// propfindBody asks for the three properties a listing needs and nothing else.
// A server answering allprop returns everything it knows, which on Nextcloud is
// a great deal.
const propfindBody = `<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:">
  <d:prop><d:getcontentlength/><d:getlastmodified/><d:resourcetype/></d:prop>
</d:propfind>`

func (d *webdavTransport) List(ctx context.Context) ([]Object, error) {
	req, err := d.request(ctx, "PROPFIND", d.base.String(), strings.NewReader(propfindBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Depth", "1")
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")

	resp, err := d.do(req, http.StatusMultiStatus, http.StatusOK)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var ms multistatus
	// Bounded: a listing is a few hundred entries, and an answer larger than
	// this is a server sending something other than a listing.
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&ms); err != nil {
		return nil, fmt.Errorf("read the listing from %s: %w", d.base.Host, err)
	}

	var out []Object
	for _, entry := range ms.Responses {
		href, err := url.PathUnescape(entry.Href)
		if err != nil {
			continue
		}
		name := path.Base(strings.TrimSuffix(href, "/"))
		if _, ours := TimeFromName(name); !ours {
			continue
		}
		obj := Object{Name: name}
		for _, p := range entry.Props {
			if p.Collection != nil {
				obj.Name = ""
				break
			}
			if p.Length > 0 {
				obj.Size = p.Length
			}
			if p.LastModified != "" {
				if at, err := http.ParseTime(p.LastModified); err == nil {
					obj.ModTime = at
				}
			}
		}
		if obj.Name == "" {
			continue
		}
		out = append(out, obj)
	}
	return out, nil
}

func (d *webdavTransport) Delete(ctx context.Context, name string) error {
	if _, ours := TimeFromName(name); !ours {
		return fmt.Errorf("backup: %q is not an archive mcpd wrote", name)
	}
	req, err := d.request(ctx, http.MethodDelete, d.url(name), nil)
	if err != nil {
		return err
	}
	resp, err := d.do(req, http.StatusOK, http.StatusNoContent, http.StatusNotFound)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (d *webdavTransport) Check(ctx context.Context) error {
	if _, err := d.List(ctx); err != nil {
		// A folder that is not there yet is the one failure worth acting on
		// rather than reporting: an operator who typed a path under a share
		// they own means for it to exist.
		var refused *webdavError
		if errors.As(err, &refused) && refused.status == http.StatusNotFound {
			if mkErr := d.mkcol(ctx); mkErr != nil {
				return err
			}
		} else {
			return err
		}
	}

	// The name is deliberately not one TimeFromName parses, so a probe left by
	// an interrupted check is never counted as a backup or deleted as one.
	probe := ".mcpd-check"
	req, err := d.request(ctx, http.MethodPut, d.url(probe), strings.NewReader("mcpd"))
	if err != nil {
		return err
	}
	req.ContentLength = 4
	resp, err := d.do(req, http.StatusOK, http.StatusCreated, http.StatusNoContent)
	if err != nil {
		return err
	}
	resp.Body.Close()

	del, err := d.request(ctx, http.MethodDelete, d.url(probe), nil)
	if err != nil {
		return err
	}
	resp, err = d.do(del, http.StatusOK, http.StatusNoContent, http.StatusNotFound)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// mkcol creates the collection, once, from Check and nowhere else. A run never
// creates a folder: a path that has stopped existing between one night and the
// next is a share that failed to mount, and making a directory on top of the
// mountpoint is how a backup ends up on the wrong disk.
func (d *webdavTransport) mkcol(ctx context.Context) error {
	req, err := d.request(ctx, "MKCOL", d.base.String(), nil)
	if err != nil {
		return err
	}
	resp, err := d.do(req, http.StatusOK, http.StatusCreated)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (d *webdavTransport) Close() error {
	d.client.CloseIdleConnections()
	return nil
}
