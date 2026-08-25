package observium

import (
	"strings"
	"testing"
	"time"
)

// The database backend has no transport to refuse a write, so the read-only
// guarantee rests on the account's grants instead. Getting this check wrong in
// either direction is bad: too loose and mcpd connects with an account that
// can drop the monitoring database, too strict and nobody can configure it.
func TestGrantCheck(t *testing.T) {
	for _, tc := range []struct {
		name    string
		grant   string
		refused bool
	}{
		{"select only", "GRANT SELECT ON `observium`.* TO `ro`@`%`", false},
		{"usage is not a privilege", "GRANT USAGE ON *.* TO `ro`@`%`", false},
		{"select on named tables", "GRANT SELECT ON `observium`.`devices` TO `ro`@`%`", false},

		{"all privileges", "GRANT ALL PRIVILEGES ON `observium`.* TO `rw`@`%`", true},
		{"insert", "GRANT SELECT, INSERT ON `observium`.* TO `rw`@`%`", true},
		{"delete", "GRANT SELECT, DELETE ON `observium`.* TO `rw`@`%`", true},
		{"drop", "GRANT DROP ON `observium`.* TO `rw`@`%`", true},
		{"grant option", "GRANT SELECT ON `observium`.* TO `ro`@`%` WITH GRANT OPTION", true},

		// The privilege list ends at ON. Without that, a database or host name
		// containing a privilege word trips the check -- and a refusal nobody
		// can explain is worse than one that never fires.
		{"database named after a privilege", "GRANT SELECT ON `create_backup`.* TO `ro`@`%`", false},
		{"host named after a privilege", "GRANT SELECT ON `observium`.* TO `ro`@`insert.example.com`", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refused := grantIsWrite(tc.grant)
			if refused != tc.refused {
				t.Fatalf("grantIsWrite(%q) = %v, want %v", tc.grant, refused, tc.refused)
			}
		})
	}
}

// MySQL hands back []byte for text and for anything it is unsure of, and a
// []byte marshals to base64 -- so a hostname would reach the model as
// "cm91dGVyLTEubG9jYWw=" unless it is converted. Numbers have to stay numbers:
// a model asked whether a disk is over 90 per cent should be comparing
// against a number rather than against text.
func TestNormalise(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   any
		want any
	}{
		{"text", []byte("router-1.example.com"), "router-1.example.com"},
		{"integer stored as text", []byte("42"), int64(42)},
		{"float stored as text", []byte("93.5"), 93.5},
		{"negative", []byte("-1"), int64(-1)},
		{"empty stays a string", []byte(""), ""},
		{"null", nil, nil},
		{"already a number", int64(7), int64(7)},
		// A version like 15.2 parses as a float, which is correct here: it is
		// what the column holds. A version like 15.2.1 does not, and must not
		// be mangled into one.
		{"dotted version stays text", []byte("15.2.1"), "15.2.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalise(tc.in); got != tc.want {
				t.Fatalf("normalise(%v) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

// Timestamps become RFC 3339 in UTC, which is a string a model can reason
// about rather than a Go struct that marshals to something it cannot.
func TestNormalise_Timestamps(t *testing.T) {
	at := time.Date(2026, 8, 24, 19, 30, 0, 0, time.UTC)
	if got := normalise(at); got != "2026-08-24T19:30:00Z" {
		t.Fatalf("normalise(time) = %v, want RFC 3339 in UTC", got)
	}
}

// Every column these queries name must exist in the schema they were written
// against, and every entity a tool can ask for must have a query. A tool that
// reaches an entity with no mapping fails inside a conversation; caught here
// it is a compile-time-ish check somebody sees first.
func TestSchema_CoversEveryEntityTheToolsUse(t *testing.T) {
	used := []Entity{
		EntityDevices, EntityPorts, EntitySensors, EntityAlerts, EntityAlertLog,
		EntityStorage, EntityMempools, EntityProcessors, EntityInventory,
		EntityNeighbours, EntityAddresses, EntityVLANs,
	}
	for _, e := range used {
		if _, ok := schema[e]; !ok {
			t.Errorf("the database backend has no query for %s", e)
		}
		if _, ok := apiPaths[e]; !ok {
			t.Errorf("the API backend has no endpoint for %s", e)
		}
	}
}

// A wildcard SELECT would hand a model the SNMP community strings and auth
// passwords sitting in the devices table beside the fields we want.
func TestSchema_NeverReadsCredentialColumns(t *testing.T) {
	forbidden := []string{
		"snmp_community", "snmp_authpass", "snmp_cryptopass", "snmp_authname",
		"snmp_context", "password", "auth",
	}
	for entity, q := range schema {
		for _, col := range q.columns {
			lower := strings.ToLower(col)
			for _, bad := range forbidden {
				if strings.Contains(lower, bad) {
					t.Errorf("%s reads %q, which is a credential", entity, col)
				}
			}
		}
	}
}

// Observium soft-deletes. A port pulled out of a switch stays in the table
// with deleted=1, so a query that ignores that reports interfaces which no
// longer exist as though they were live.
func TestSchema_SoftDeletedTablesSaySo(t *testing.T) {
	for _, e := range []Entity{EntityPorts, EntitySensors, EntityStorage,
		EntityMempools, EntityInventory} {
		if schema[e].deleted == "" {
			t.Errorf("%s has a soft-delete column in Observium's schema and "+
				"this query does not filter on it", e)
		}
	}
}
