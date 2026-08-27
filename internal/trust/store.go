package trust

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/spoked/mcpd/internal/storage/sqlite"
)

// Store holds the certificates this host trusts in addition to the system
// roots.
type Store struct {
	db  *sqlite.DB
	now func() time.Time
}

// NewStore returns a store backed by db.
func NewStore(db *sqlite.DB, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, now: now}
}

// Add parses raw, stores it under name, and records who did it.
//
// Parsing before storing is the point: an unreadable paste is refused here,
// where somebody is looking at the form, rather than at a handshake weeks
// later with nothing to connect it to.
func (s *Store) Add(ctx context.Context, actor, name string, raw []byte) (*Certificate, error) {
	name, err := ValidateName(name)
	if err != nil {
		return nil, err
	}
	cert, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	cert.ID, cert.Name, cert.AddedBy = id, name, actor

	now := s.now().UnixMilli()
	cert.AddedAt = time.UnixMilli(now).UTC()

	err = s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		// Both conditions are in the statement rather than in reads before
		// it: two administrators adding the same certificate at once would
		// each read a table without the other's row and both proceed.
		affected, err := tx.ExecAffected(`
			INSERT INTO ca_certificates
			       (id, name, pem, subject, issuer, fingerprint,
			        not_before, not_after, is_ca, basic_constraints_valid,
			        key_usage, added_by, added_at)
			SELECT ?,?,?,?,?,?,?,?,?,?,?,?,?
			 WHERE NOT EXISTS (SELECT 1 FROM ca_certificates WHERE lower(name) = lower(?))
			   AND NOT EXISTS (SELECT 1 FROM ca_certificates WHERE fingerprint = ?)`,
			cert.ID, cert.Name, cert.PEM, cert.Subject, cert.Issuer, cert.Fingerprint,
			cert.NotBefore.UnixMilli(), cert.NotAfter.UnixMilli(),
			boolToInt(cert.IsCA), boolToInt(cert.BasicConstraintsValid),
			int64(cert.KeyUsage), actor, now,
			cert.Name, cert.Fingerprint)
		if err != nil {
			return err
		}
		if affected == 0 {
			// Which of the two conditions failed decides what the operator
			// should do next, and the insert cannot say. One read, only on the
			// path that is already refusing.
			return s.explainRefusal(ctx, cert)
		}
		return tx.AppendAudit(sqlite.AdminAct{
			Kind:    "certificate.added",
			Actor:   actor,
			Subject: cert.Name,
			Action:  "create",
			Detail: map[string]any{
				"certificate": cert.ID,
				"subject":     cert.Subject,
				"fingerprint": cert.Fingerprint,
				"expires_at":  cert.NotAfter.Format(time.RFC3339),
			},
		})
	})
	if err != nil {
		return nil, err
	}
	return cert, nil
}

// explainRefusal turns a guarded insert that matched nothing into the sentence
// that says why.
func (s *Store) explainRefusal(ctx context.Context, cert *Certificate) error {
	var existing string
	err := s.db.Reader().QueryRowContext(ctx,
		`SELECT name FROM ca_certificates WHERE fingerprint = ?`,
		cert.Fingerprint).Scan(&existing)
	switch {
	case err == nil:
		return fmt.Errorf("%w, under the name %q", ErrDuplicateCertificate, existing)
	case errors.Is(err, sql.ErrNoRows):
		return ErrDuplicateName
	default:
		return err
	}
}

const certificateColumns = `id, name, pem, subject, issuer, fingerprint,
	not_before, not_after, is_ca, basic_constraints_valid, key_usage,
	added_by, added_at`

// List returns every stored certificate, soonest to expire first.
//
// Not alphabetical: the question this page is opened to answer is which one is
// about to break something, and an expiry that has to be hunted for in a
// list sorted by name is one nobody sees until an integration stops.
func (s *Store) List(ctx context.Context) ([]*Certificate, error) {
	rows, err := s.db.Reader().QueryContext(ctx,
		`SELECT `+certificateColumns+` FROM ca_certificates ORDER BY not_after, lower(name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []*Certificate{}
	for rows.Next() {
		c, err := scanCertificate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ByID loads one certificate.
func (s *Store) ByID(ctx context.Context, id string) (*Certificate, error) {
	return scanCertificate(s.db.Reader().QueryRowContext(ctx,
		`SELECT `+certificateColumns+` FROM ca_certificates WHERE id = ?`, id))
}

// Delete removes a certificate.
//
// Withdrawing trust takes effect on the next connection, without a restart:
// the host rebuilds the pool and remounts what was using it. Trust is the one
// thing an operator may need to take back immediately, so nothing here can
// refuse the removal on the grounds that something is still relying on it.
func (s *Store) Delete(ctx context.Context, actor, id string) error {
	cert, err := s.ByID(ctx, id)
	if err != nil {
		return err
	}
	now := s.now().UnixMilli()
	return s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		affected, err := tx.ExecAffected(`DELETE FROM ca_certificates WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if affected == 0 {
			return ErrNotFound
		}
		return tx.AppendAudit(sqlite.AdminAct{
			Kind:    "certificate.removed",
			Actor:   actor,
			Subject: cert.Name,
			Action:  "delete",
			Detail: map[string]any{
				"certificate": cert.ID,
				"subject":     cert.Subject,
				"fingerprint": cert.Fingerprint,
			},
		})
	})
}

// Pool builds the roots outbound connections verify against: the system store,
// plus everything stored here.
//
// Everything, rather than a set each integration opts into. A certificate
// added on this page is one an operator has decided this host should trust,
// and the alternative -- adding it and then naming it again on every plugin
// that needs it -- fails in the way nobody catches: the certificate is there,
// the handshake still fails, and the error is the same one they were trying to
// fix. It is the arrangement a company CA in an operating system's trust store
// already has, which is the model an administrator adding one is working from.
//
// Additive, never a replacement. An instance behind a company CA still reaches
// public endpoints -- a cloud API on an ordinary certificate is the common
// case -- so a pool that held only these would break the integrations nobody
// touched.
//
// Nil, nil when nothing is stored, which is Go's own "use the system roots"
// and keeps the untouched deployment free of a pool built for nothing.
func (s *Store) Pool(ctx context.Context) (*x509.CertPool, error) {
	certs, err := s.List(ctx)
	if err != nil || len(certs) == 0 {
		return nil, err
	}
	return Pool(certs)
}

// Pool is the pool builder, separated from the store so it can be tested and
// used without a database.
func Pool(certs []*Certificate) (*x509.CertPool, error) {
	if len(certs) == 0 {
		return nil, nil
	}
	// Failing rather than starting from an empty pool. A host that cannot read
	// its system roots would otherwise quietly stop trusting every public
	// certificate the moment somebody added a company CA, and the breakage
	// would land on the integrations nobody changed.
	pool, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("trust: cannot read the system certificate pool, "+
			"so the extra certificates cannot be added to it: %w", err)
	}
	for _, c := range certs {
		if !pool.AppendCertsFromPEM([]byte(c.PEM)) {
			return nil, fmt.Errorf("trust: %q could not be added to the pool; "+
				"it parsed when it was stored, so the stored bytes are the "+
				"thing to look at", c.Name)
		}
	}
	return pool, nil
}

// scanner is what QueryRow and Rows have in common.
type scanner interface{ Scan(dest ...any) error }

func scanCertificate(row scanner) (*Certificate, error) {
	var (
		c                   Certificate
		notBefore, notAfter int64
		addedAt             int64
		isCA, bcValid       int
		keyUsage            int64
	)
	err := row.Scan(&c.ID, &c.Name, &c.PEM, &c.Subject, &c.Issuer, &c.Fingerprint,
		&notBefore, &notAfter, &isCA, &bcValid, &keyUsage, &c.AddedBy, &addedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.NotBefore = time.UnixMilli(notBefore).UTC()
	c.NotAfter = time.UnixMilli(notAfter).UTC()
	c.AddedAt = time.UnixMilli(addedAt).UTC()
	c.IsCA = isCA == 1
	c.BasicConstraintsValid = bcValid == 1
	c.KeyUsage = x509.KeyUsage(keyUsage)
	return &c, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("trust: system entropy unavailable: %w", err)
	}
	return "crt_" + hex.EncodeToString(b), nil
}
