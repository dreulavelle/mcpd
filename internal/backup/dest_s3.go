package backup

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Anything speaking S3: AWS, Cloudflare R2, Backblaze B2, Wasabi, MinIO,
// Hetzner, DigitalOcean Spaces. One form covers all of them.
//
// minio-go rather than the AWS SDK, and rather than signing requests here.
// SigV4 is not the hard part -- the hard part is that the AWS SDK sends a
// CRC32 checksum header by default that R2 and B2 reject, that R2 wants the
// region spelled `auto`, and that B2 and MinIO want path-style addressing. Each
// of those is a day somebody loses to a service returning a 400 with no
// explanation. minio-go is written against exactly this spread of
// implementations.

type s3Transport struct {
	client *minio.Client
	bucket string
	prefix string
}

func openS3(d Destination, opts TransportOptions) (Transport, error) {
	if strings.TrimSpace(d.Settings.Bucket) == "" {
		return nil, errors.New("backup: this destination has no bucket")
	}

	// Cloudflare R2 has one region and calls it `auto`. Filling it in rather
	// than refusing an empty box is the difference between a form that works
	// and a form that needs the documentation open beside it.
	region := strings.TrimSpace(d.Settings.Region)
	if region == "" {
		region = "auto"
	}

	lookup := minio.BucketLookupDNS
	if d.Settings.PathStyle {
		lookup = minio.BucketLookupPath
	}

	secure := !d.Settings.AllowInsecure
	client, err := minio.New(strings.TrimSpace(d.Settings.Endpoint), &minio.Options{
		Creds: credentials.NewStaticV4(
			strings.TrimSpace(d.Settings.AccessKey), d.Secret, ""),
		Secure:       secure,
		Region:       region,
		BucketLookup: lookup,
		Transport:    s3HTTPTransport(opts, secure),
	})
	if err != nil {
		return nil, fmt.Errorf("backup: configure %s: %w", d.Settings.Endpoint, err)
	}
	return &s3Transport{
		client: client,
		bucket: strings.TrimSpace(d.Settings.Bucket),
		prefix: s3Prefix(d.Settings.Prefix),
	}, nil
}

// s3HTTPTransport builds the HTTP transport, carrying this host's own trusted
// roots so a MinIO behind a company authority or an appliance's own
// certificate is reachable without anybody being told to switch verification
// off.
func s3HTTPTransport(opts TransportOptions, secure bool) http.RoundTripper {
	if !secure || opts.Pool == nil {
		// nil leaves minio-go to build its own default, which is what a host
		// with nothing extra to trust wants.
		return nil
	}
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.TLSClientConfig = &tls.Config{RootCAs: opts.Pool, MinVersion: tls.VersionTLS12}
	return base
}

// s3Prefix normalises the folder inside the bucket: no leading slash, one
// trailing one, and empty stays empty.
func s3Prefix(p string) string {
	p = strings.Trim(strings.TrimSpace(p), "/")
	if p == "" {
		return ""
	}
	return p + "/"
}

func (s *s3Transport) key(name string) string { return s.prefix + name }

func (s *s3Transport) Put(ctx context.Context, name string, r io.Reader, size int64) error {
	_, err := s.client.PutObject(ctx, s.bucket, s.key(name), r, size,
		minio.PutObjectOptions{ContentType: "application/octet-stream"})
	if err != nil {
		return fmt.Errorf("upload to %s: %w", s.bucket, err)
	}
	return nil
}

func (s *s3Transport) List(ctx context.Context) ([]Object, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var out []Object
	for obj := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
		Prefix: s.prefix, Recursive: false,
	}) {
		if obj.Err != nil {
			return nil, fmt.Errorf("list %s: %w", s.bucket, obj.Err)
		}
		name := path.Base(obj.Key)
		if _, ours := TimeFromName(name); !ours {
			continue
		}
		out = append(out, Object{Name: name, Size: obj.Size, ModTime: obj.LastModified})
	}
	return out, nil
}

func (s *s3Transport) Delete(ctx context.Context, name string) error {
	if _, ours := TimeFromName(name); !ours {
		return fmt.Errorf("backup: %q is not an archive mcpd wrote", name)
	}
	if err := s.client.RemoveObject(ctx, s.bucket, s.key(name),
		minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("remove %s: %w", name, err)
	}
	return nil
}

func (s *s3Transport) Check(ctx context.Context) error {
	if _, err := s.List(ctx); err != nil {
		return err
	}
	// A credential with ListBucket and no PutObject lists happily. The probe is
	// what tells an operator now rather than at four in the morning.
	//
	// The name is deliberately not one TimeFromName parses, so a probe left
	// behind by an interrupted check can never be counted as a backup or
	// deleted as one.
	probe := s.prefix + ".mcpd-check"
	body := strings.NewReader("mcpd")
	if _, err := s.client.PutObject(ctx, s.bucket, probe, body, int64(body.Len()),
		minio.PutObjectOptions{ContentType: "text/plain"}); err != nil {
		return fmt.Errorf("write to %s: %w", s.bucket, err)
	}
	if err := s.client.RemoveObject(ctx, s.bucket, probe,
		minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("remove the test file from %s: %w", s.bucket, err)
	}
	return nil
}

func (s *s3Transport) Close() error { return nil }
