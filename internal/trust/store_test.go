package trust

import (
	"context"
	"crypto/x509"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/storage/sqlite"
)

var testClock = time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)

func newStore(t *testing.T) (*Store, *sqlite.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.Open(ctx, sqlite.Options{
		Path:              filepath.Join(t.TempDir(), "trust.db"),
		RelaxedDurability: true,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := sqlite.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewStore(db, func() time.Time { return testClock }), db
}

func TestStore_AddAndRead(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	_, pemBytes := makeCert(t, certOptions{commonName: "Work CA", ip: "10.10.12.53"})

	added, err := s.Add(ctx, "user:alice", "Work CA", pemBytes)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if added.ID == "" || added.AddedBy != "user:alice" {
		t.Errorf("stored certificate = %+v, want an id and the actor", added)
	}

	back, err := s.ByID(ctx, added.ID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if back.Fingerprint != added.Fingerprint || back.PEM != added.PEM {
		t.Error("what came back is not what went in")
	}
	// The facts that decide whether trusting it can work have to survive the
	// round trip, or CanAnchor answers a different question after a restart
	// than it did on the page that stored it.
	if back.CanAnchor() != added.CanAnchor() {
		t.Errorf("CanAnchor() = %v after a reload, was %v", back.CanAnchor(), added.CanAnchor())
	}
}

// A leaf that says CA:FALSE cannot anchor a chain. Storing it is allowed --
// the operator may know something this does not -- but the fact has to come
// back out of the database, because it is what the page warns with.
func TestStore_KeepsWhatDecidesAnchoring(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	_, leaf := makeCert(t, certOptions{
		commonName: "just a leaf", basicConstraintsValid: true,
		keyUsage: x509.KeyUsageDigitalSignature,
	})

	added, err := s.Add(ctx, "user:alice", "Leaf", leaf)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	back, err := s.ByID(ctx, added.ID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if back.CanAnchor() {
		t.Error("a certificate that says CA:FALSE came back claiming it can anchor a chain")
	}
	if !back.IsCAExplicitlyFalse() {
		t.Error("the difference between saying CA:FALSE and saying nothing was lost")
	}
}

func TestStore_RefusesADuplicateName(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	_, first := makeCert(t, certOptions{commonName: "One"})
	_, second := makeCert(t, certOptions{commonName: "Two"})

	if _, err := s.Add(ctx, "user:alice", "Work CA", first); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Case-insensitively: two entries called "Work CA" and "work ca" are one
	// certificate as far as anybody reading the list is concerned.
	_, err := s.Add(ctx, "user:alice", "work ca", second)
	if !errors.Is(err, ErrDuplicateName) {
		t.Fatalf("err = %v, want a duplicate name", err)
	}
}

// The same certificate under a second name would be two rows describing one
// trust decision. The refusal names the row that already has it, because the
// answer is "you already have it, called X" rather than "no".
func TestStore_RefusesTheSameCertificateTwiceAndSaysWhereItIs(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	_, pemBytes := makeCert(t, certOptions{commonName: "Work CA"})

	if _, err := s.Add(ctx, "user:alice", "Work CA", pemBytes); err != nil {
		t.Fatalf("Add: %v", err)
	}
	_, err := s.Add(ctx, "user:bob", "Work CA copy", pemBytes)
	if !errors.Is(err, ErrDuplicateCertificate) {
		t.Fatalf("err = %v, want a duplicate certificate", err)
	}
	if !strings.Contains(err.Error(), "Work CA") {
		t.Errorf("message = %q, want it to name the certificate already stored", err)
	}
}

// Soonest to expire first: the question this page is opened to answer is which
// one is about to break something.
func TestStore_ListsSoonestToExpireFirst(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	_, late := makeCert(t, certOptions{
		commonName: "late", notAfter: testClock.Add(365 * 24 * time.Hour),
	})
	_, soon := makeCert(t, certOptions{
		commonName: "soon", notAfter: testClock.Add(10 * 24 * time.Hour),
	})

	if _, err := s.Add(ctx, "user:alice", "Zulu", late); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := s.Add(ctx, "user:alice", "Alpha", soon); err != nil {
		t.Fatalf("Add: %v", err)
	}

	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List returned %d, want 2", len(list))
	}
	if list[0].Name != "Alpha" {
		t.Errorf("first is %q, want the one expiring soonest even though it sorts first "+
			"alphabetically by accident here", list[0].Name)
	}
}

// Trust is the one thing an operator may need to take back immediately, so
// nothing refuses the removal.
func TestStore_Delete(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	_, pemBytes := makeCert(t, certOptions{})

	added, err := s.Add(ctx, "user:alice", "Work CA", pemBytes)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := s.Delete(ctx, "user:alice", added.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.ByID(ctx, added.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want not found", err)
	}
	if err := s.Delete(ctx, "user:alice", added.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting it twice gave %v, want not found", err)
	}
}

// Adding and removing are both trust decisions, and the audit trail is where
// "who decided this host should believe that authority" is answered.
func TestStore_AuditsBothDirections(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()
	_, pemBytes := makeCert(t, certOptions{})

	added, err := s.Add(ctx, "user:alice", "Work CA", pemBytes)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := s.Delete(ctx, "user:bob", added.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// The subject of an administrative act lands in the trail's "which thing"
	// column, which is named `plugin` because that is what it held first.
	rows, err := db.Reader().QueryContext(ctx,
		`SELECT kind, actor, plugin FROM audit_events ORDER BY rowid`)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var kind, actor, subject string
		if err := rows.Scan(&kind, &actor, &subject); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, kind+" "+actor+" "+subject)
	}
	want := []string{
		"certificate.added user:alice Work CA",
		"certificate.removed user:bob Work CA",
	}
	if len(got) != len(want) {
		t.Fatalf("audit = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("audit[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// Nothing stored is not a pool with nothing in it: it is no pool, which is
// Go's own "use the system roots".
func TestStore_PoolIsNilUntilSomethingIsStored(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()

	pool, err := s.Pool(ctx)
	if err != nil {
		t.Fatalf("Pool: %v", err)
	}
	if pool != nil {
		t.Fatal("a pool was built with nothing stored")
	}

	_, pemBytes := makeCert(t, certOptions{})
	if _, err := s.Add(ctx, "user:alice", "Work CA", pemBytes); err != nil {
		t.Fatalf("Add: %v", err)
	}
	pool, err = s.Pool(ctx)
	if err != nil {
		t.Fatalf("Pool: %v", err)
	}
	if pool == nil {
		t.Fatal("nothing was built for a stored certificate")
	}
}
