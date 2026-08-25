package observium

import "sort"

// Observium answers with the whole database row, credentials included.
//
// A real /devices answer from a level 6 token carries 69 fields per device.
// Twenty of them are null, several are poller bookkeeping, one is a multi-line
// sysDescr banner -- and four are the SNMP credential the device is polled
// with. Measured against a live installation, five devices cost 21KB of tool
// result; the same call across an 85-device estate would be around 169KB, or
// most of a context window, to answer "which hosts are down".
//
// Two separate problems live in that sentence, and they need separate fixes.
//
// The credential is not a size problem. It must not reach a model whatever
// view was asked for, including the one that promises full detail, so it is
// removed first and unconditionally -- see alwaysRemoved. The integration test
// has asserted this since before it could be enforced: "a credential must
// never appear, whatever the API decides to include". It appears. This is what
// makes that assertion true rather than aspirational.
//
// The size is the second problem, and a listing solves it by returning a view
// rather than a row. The view is an allow-list rather than a deny-list because
// the same reasoning applies as above: a deny-list protects against the field
// names somebody thought of, and this API is explicitly open-ended -- the
// contract declares only device_id and hostname, with additionalProperties
// true for the rest.
//
// Projection happens here rather than upstream. /devices and /ports accept a
// fields parameter and the other twenty-odd endpoints do not, so relying on it
// would leave most entities unprojected; the encoding it wants is undocumented
// besides. Trimming what arrived is uniform, needs nothing of the far end, and
// cannot break a read by guessing wrong. It saves context rather than
// bandwidth. Sending fields upstream as well would save both and is worth
// adding, but it is an optimisation on top of this, never a replacement for
// it: a credential that must not be returned must not be relied upon to be
// withheld by the far end.
type view string

const (
	// viewSummary keeps the fields that answer the question a listing asks.
	viewSummary view = "summary"
	// viewFull keeps the row as Observium sent it, less what is never
	// returned, for the tools that promise full detail about one named thing.
	viewFull view = "full"
)

// alwaysRemoved are fields no view returns.
//
// These are the credential a device is polled with. Observium hands them to
// any token whose account can read the device, so nothing upstream is going to
// withhold them and this is the only place it can be done. A community string
// in a tool result is a live SNMP credential in a model's context, and from
// there in whatever the transcript reaches.
//
// snmp_authname is on the list with the passwords. It is the SNMPv3 username,
// which alone is not a secret -- but it is half of a credential, it is of no
// use in answering any question this plugin exists to answer, and the test
// that named these four named it too.
var alwaysRemoved = []string{
	"snmp_community",
	"snmp_authpass",
	"snmp_cryptopass",
	"snmp_authname",
}

// summaryFields is what a listing keeps, per entity.
//
// Deliberately generous across spellings of the same fact -- both
// ifInOctets_rate and in_rate, both sensor_descr and sensor_type -- because
// Observium's column names vary by version and a name it does not use is
// simply absent from the output. Listing a name that turns out not to exist
// costs nothing; omitting one that does costs a field somebody needed. That
// asymmetry is the only reason these lists are longer than they look.
//
// The device set is the one checked against a live installation. The rest are
// written from the same API's naming conventions and the filters the tools
// already send, and are the ones to correct first if a field somebody wanted
// turns out to be missing.
//
// An entity with no entry here is not narrowed: it arrives whole, less
// alwaysRemoved. That is the right default for an entity type nobody has
// chosen a field set for, because failing to narrow shows up as a large answer
// while narrowing to a set nobody checked is a quiet hole in one.
var summaryFields = map[Entity][]string{
	EntityDevices: {
		"device_id", "hostname", "sysName", "os", "version", "hardware",
		"vendor", "type", "purpose", "serial", "status", "status_type",
		"uptime", "last_polled", "location", "disabled", "ignore",
	},
	// /ports returns 143 fields: the port's own columns plus the whole device
	// row joined onto every one of them -- sysDescr, location, poller timings,
	// SNMP transport. This is the listing that most needed narrowing.
	EntityPorts: {
		"port_id", "device_id", "hostname", "ifIndex", "ifName", "ifDescr",
		"ifAlias", "port_label", "ifOperStatus", "ifAdminStatus", "ifSpeed",
		"ifMtu", "ifType", "ifVlan", "ifInErrors", "ifOutErrors",
		"ifInDiscards", "ifOutDiscards", "ifInOctets", "ifOutOctets",
		"ifInOctets_rate", "ifOutOctets_rate", "ifInErrors_rate",
		"ifOutErrors_rate", "in_rate", "out_rate", "poll_time", "ifLastChange",
	},
	EntitySensors: {
		"sensor_id", "device_id", "sensor_class", "sensor_type", "sensor_descr",
		"sensor_value", "sensor_unit", "sensor_limit", "sensor_limit_low",
		"sensor_limit_warn", "sensor_limit_low_warn", "sensor_event",
		"sensor_status", "sensor_last_change", "sensor_ignore", "sensor_disable",
		"sensor_polled", "measured_class", "measured_entity",
		"measured_entity_label",
	},
	// severity, not alert_severity, and there is no entity_name: the alert
	// names its entity by type and id and leaves the lookup to entity_cache.
	EntityAlerts: {
		"alert_table_id", "device_id", "alert_test_id", "entity_type",
		"entity_id", "alert_status", "status", "state", "severity", "class",
		"alerted", "count", "last_message", "last_changed", "last_checked",
		"last_alerted", "last_failed", "last_ok", "last_recovered",
		"ignore_until",
	},
	EntityStorage: {
		"storage_id", "device_id", "storage_descr", "storage_type",
		"storage_size", "storage_used", "storage_free", "storage_perc",
		"storage_units", "storage_ignore", "storage_polled",
	},
	EntityMempools: {
		"mempool_id", "device_id", "mempool_descr", "mempool_total",
		"mempool_used", "mempool_free", "mempool_perc", "mempool_ignore",
		"mempool_polled",
	},
	EntityProcessors: {
		"processor_id", "device_id", "processor_descr", "processor_type",
		"processor_usage", "processor_ignore", "processor_polled",
	},
	EntityInventory: {
		"entPhysical_id", "device_id", "entPhysicalClass", "entPhysicalName",
		"entPhysicalDescr", "entPhysicalAlias", "entPhysicalModelName",
		"entPhysicalMfgName", "entPhysicalSerialNum", "entPhysicalHardwareRev",
		"entPhysicalSoftwareRev", "entPhysicalFirmwareRev",
		"entPhysicalContainedIn", "entPhysicalIndex", "ifIndex",
	},
	EntityAddresses: {
		"ipv4_address_id", "ipv6_address_id", "device_id", "port_id", "ifIndex",
		"ipv4_address", "ipv6_address", "ipv4_prefixlen", "ipv6_prefixlen",
		"ipv4_network", "ipv6_network", "ipv4_type", "ipv6_type", "vrf_id",
	},
	// Two sets that could not be checked: this estate has no discovered
	// neighbours and an empty alert log, so both endpoints answered with
	// nothing to compare against. They are written from the same conventions
	// the checked sets follow, and are the first place to look if either tool
	// comes back thinner than it should.
	EntityNeighbours: {
		"neighbour_id", "device_id", "port_id", "remote_hostname",
		"remote_port", "remote_platform", "remote_version", "protocol",
		"remote_port_id", "remote_device_id",
	},
	EntityAlertLog: {
		"event_id", "device_id", "entity_type", "entity_id", "alert_test_id",
		"log_type", "message", "timestamp", "status", "severity",
	},

	// Checked against the live installation, like the sets above.
	EntityAlertChecks: {
		"alert_test_id", "alert_name", "alert_message", "entity_type",
		"entity_status", "severity", "class", "conditions", "conditions_warn",
		"and", "delay", "enable", "alerter", "num_entities", "status_numbers",
		"ignore_until", "suppress_recovery", "notification_schedule",
		"alert_assoc",
	},
	// 48 fields, of which three quarters are the OID, MIB and multiplier
	// Observium polled the value with, plus four pre-rendered spellings of
	// the same rate. The rate and the value are kept once each, as numbers.
	EntityCounters: {
		"counter_id", "device_id", "counter_class", "counter_descr",
		"counter_value", "counter_rate", "counter_rate_5min",
		"counter_rate_hour", "counter_unit", "counter_event", "counter_status",
		"counter_limit", "counter_limit_low", "counter_limit_low_warn",
		"counter_limit_high_warn", "counter_last_change", "counter_polled",
		"counter_ignore", "counter_disable", "severity", "event_descr",
		"measured_class", "measured_entity", "measured_entity_label",
	},
	EntityStatus: {
		"status_id", "device_id", "status_name", "status_descr", "status_type",
		"status_value", "status_event", "status_last_change", "status_polled",
		"status_ignore", "status_disable", "severity", "event_class",
		"event_descr", "measured_class", "measured_entity",
		"measured_entity_label",
	},
	EntityPrinterSupply: {
		"supply_id", "device_id", "supply_descr", "supply_type",
		"supply_colour", "supply_capacity", "supply_value", "supply_index",
	},

	// Four entities have no set: bills, power_bills, maintenance and probes.
	// This estate has none of any of them, so there was nothing to check a
	// set against, and they arrive whole until there is. Their rows are the
	// small ones -- a bill is a quota and a counter, not a polled entity with
	// an OID for every field.

	// EntityVLANs is deliberately absent. /vlans does not answer with a list
	// of VLAN entities at all -- it returns one aggregate of counts, devices
	// and names, which is a shape a per-entity allow-list cannot describe. It
	// arrives whole, and it is small.
}

// narrow removes what no view returns, then applies the view.
//
// It reports how many fields the widest item lost, so a caller can say so. A
// model told it received seventeen of sixty-nine fields can ask for the rest;
// one handed a trimmed row silently will answer as though the row were all
// there was -- the same reason a truncated listing says so rather than logging
// it.
//
// Credentials are already gone by the time this runs -- walk removes them
// before an item is retained -- so the deletion here is a second pass over an
// empty set. It stays because this function is the one that says what a view
// returns, and a view that promises no credentials should not depend on
// another function having kept that promise.
func narrow(items []map[string]any, entity Entity, v view) (kept []string, dropped int) {
	allow, narrowing := allowFor(entity, v)

	seen := map[string]bool{}
	widest := 0
	for _, item := range items {
		if len(item) > widest {
			widest = len(item)
		}
		for _, name := range alwaysRemoved {
			delete(item, name)
		}
		if !narrowing {
			continue
		}
		for name := range item {
			if allow[name] {
				seen[name] = true
				continue
			}
			delete(item, name)
		}
	}

	remaining := 0
	for _, item := range items {
		if len(item) > remaining {
			remaining = len(item)
		}
		if !narrowing {
			for name := range item {
				seen[name] = true
			}
		}
	}

	kept = make([]string, 0, len(seen))
	for name := range seen {
		kept = append(kept, name)
	}
	sort.Strings(kept)

	if widest > remaining {
		dropped = widest - remaining
	}
	return kept, dropped
}

// allowFor resolves the field set for a view, and whether narrowing applies at
// all. A full view, or an entity with no declared set, narrows nothing --
// alwaysRemoved still applies to both.
func allowFor(entity Entity, v view) (map[string]bool, bool) {
	if v == viewFull {
		return nil, false
	}
	fields, ok := summaryFields[entity]
	if !ok {
		return nil, false
	}
	allow := make(map[string]bool, len(fields))
	for _, name := range fields {
		allow[name] = true
	}
	return allow, true
}
