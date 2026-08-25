package observium

import (
	"context"
	"net/url"
)

// Entity is one kind of thing Observium records.
//
// It is deliberately not a URL path. The API backend turns it into one; the
// database backend turns it into a table, and neither is more "real" than the
// other. A tool asks for devices and does not learn which.
type Entity string

const (
	EntityDevices    Entity = "devices"
	EntityPorts      Entity = "ports"
	EntitySensors    Entity = "sensors"
	EntityAlerts     Entity = "alerts"
	EntityAlertLog   Entity = "alert_log"
	EntityStorage    Entity = "storage"
	EntityMempools   Entity = "mempools"
	EntityProcessors Entity = "processors"
	EntityInventory  Entity = "inventory"
	EntityNeighbours Entity = "neighbours"
	EntityAddresses  Entity = "addresses"
	EntityVLANs      Entity = "vlans"
)

// Filter names are the vocabulary a tool speaks, and they are the API's own.
//
// One of the two backends has to win, and the API's names are the ones that
// are documented, versioned and stable. The database's column names are
// neither: Observium's schema changes between releases with no compatibility
// promise, so making the tools speak SQL would put a private schema in the
// middle of a public contract. mysql.go translates; nothing else has to.
const (
	FilterDeviceID = "device_id"
	FilterHostname = "hostname"
	FilterStatus   = "status"
	FilterOS       = "os"
	FilterLocation = "location"
	FilterHardware = "hardware"
	FilterVendor   = "vendor"
	FilterGroup    = "group"
	FilterState    = "state"
	FilterErrors   = "errors"
	FilterAlerted  = "alerted"
	FilterIfAlias  = "ifAlias"
	FilterMetric   = "metric"
	FilterEvent    = "event"
	FilterMessage  = "message"
	FilterFrom     = "timestamp_from"
	FilterTo       = "timestamp_to"
	FilterModel    = "entPhysicalModelName"
	FilterSerial   = "entPhysicalSerialNum"
	// FilterID selects one entity by its own primary key. The API expresses
	// this as a path segment rather than a query parameter, which is why it is
	// named here rather than being one of the filters above.
	FilterID = "__id"
)

// Reader answers one entity query, however it reaches Observium.
//
// Both backends read the same estate and neither can write. What differs is
// what they can prove about that: the API backend refuses a non-GET at the
// transport, and the database backend has no HTTP to guard, so it checks the
// account's grants instead. Each says which in Describe, because "read-only"
// earned two different ways is two different claims.
type Reader interface {
	// Read answers a query. Filters are the names above; an unknown one is
	// ignored rather than refused, because a filter that does not apply to a
	// backend is a narrower answer and not a wrong one.
	Read(ctx context.Context, entity Entity, filters url.Values, limit int) (Page, error)

	// Probe establishes that the far end is reachable and the credential
	// works, cheaply enough to run at startup.
	Probe(ctx context.Context) error

	// Describe says how this backend reaches Observium and what its read-only
	// guarantee rests on, for the health report and the startup log.
	Describe() string

	// Close releases whatever the backend holds. The API backend holds
	// nothing; the database backend holds a connection pool.
	Close() error
}
