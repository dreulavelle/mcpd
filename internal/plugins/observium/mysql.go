package observium

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"golang.org/x/time/rate"
)

// The database backend, for Observium Community Edition.
//
// The REST API is a subscription feature. On CE there is no API to talk to at
// all, so the only way to read the estate is the database Observium writes to.
// That is a materially different thing to do, and the comments here are about
// the ways it is different rather than about SQL.
//
// # Read-only has to be proved differently
//
// The API backend's guarantee is transport.go refusing every method but GET:
// structural, and impossible to talk past. There is no HTTP here to guard, so
// that guarantee does not carry over. What replaces it is checkGrants, run at
// startup: the account's own grants are read back from MySQL and anything
// beyond SELECT refuses the connection. The guarantee therefore rests on the
// database server rather than on this code, which is stronger -- a bug here
// cannot widen it.
//
// It is not free, though, and it is worth being honest about the difference. A
// read-only API token is scoped to Observium's own permission model, so it
// sees what one Observium account may see. A MySQL account with SELECT on the
// schema sees everything, including the SNMP community strings and the
// per-user password hashes in the tables this never reads. Least privilege is
// therefore the operator's job here in a way it is not with a token, which is
// why the setting help says to grant SELECT on named tables.
//
// # The schema is not a contract
//
// Observium versions its API. It does not version its schema, and columns move
// between releases with no compatibility promise. So the columns read here are
// checked at startup against the tables actually present, and a mismatch is a
// refusal naming the column rather than a query that silently returns nothing.

// mysqlReader reads Observium's database directly.
type mysqlReader struct {
	db      *sql.DB
	cfg     Config
	log     *slog.Logger
	limiter *rate.Limiter
	cache   *readCache
	observe func(outcome string, d time.Duration)
	now     func() time.Time
}

// entityQuery says how one entity is read out of the schema.
//
// Columns are named explicitly rather than SELECT *. Three reasons, in order
// of how much they matter: the devices table holds SNMP community strings and
// auth passwords, and a wildcard would hand them to a model; a wildcard makes
// a schema change silently alter what a tool returns; and naming them is what
// lets startup verify they exist.
type entityQuery struct {
	table   string
	columns []string
	// idColumn is the primary key, used both for ordering and for FilterID.
	idColumn string
	// deleted names a soft-delete column, if the table has one. Observium
	// keeps removed ports and sensors as rows rather than deleting them, so a
	// query that ignores this reports interfaces that no longer exist.
	deleted string
	// filters maps the shared filter vocabulary onto this table's columns.
	// A filter absent here cannot be applied, which is refused rather than
	// dropped -- see selectFrom.
	filters map[string]string
	// values translates a filter's API value into what the column actually
	// holds, for the columns where the two disagree.
	//
	// They disagree more often than reading the API documentation suggests.
	// The API takes status=up for a device; the column is a tinyint holding 1.
	// The API documents sensor event "warn"; the enum's value is "warning".
	// Sending the API's word straight to MySQL matches nothing and returns an
	// empty result, which reads as "no devices are up".
	values map[string]map[string]string
	// clauses handle a filter that is not an equality against one column.
	clauses map[string]func(value string) (string, []any)
	// joinDevices adds a join to devices, for the entities whose useful
	// filters -- hostname, location, group -- live on the device rather than
	// on the row itself.
	joinDevices bool
}

// schema is the whole of what this backend knows about Observium's database.
//
// Deliberately one table per entity with no clever generalisation. A join that
// looked elegant here would be a join to debug against somebody's production
// monitoring database at the moment they most need an answer.
var schema = map[Entity]entityQuery{
	EntityDevices: {
		table:    "devices",
		idColumn: "device_id",
		columns: []string{
			"device_id", "hostname", "sysName", "sysDescr", "sysContact",
			"os", "version", "hardware", "vendor", "features", "location",
			"status", "status_type", "disabled", "ignore", "uptime",
			"last_rebooted", "last_polled", "purpose", "type", "serial",
			"distro", "distro_ver", "kernel", "arch",
		},
		filters: map[string]string{
			FilterDeviceID: "device_id",
			FilterHostname: "hostname",
			FilterOS:       "os",
			FilterLocation: "location",
			FilterHardware: "hardware",
			FilterVendor:   "vendor",
		},
		clauses: map[string]func(string) (string, []any){
			// The API spells this up/down/disabled. The schema spells it as a
			// tinyint status plus a separate disabled column, so one filter
			// reaches two columns and cannot be a value translation.
			FilterStatus: func(v string) (string, []any) {
				switch strings.ToLower(v) {
				case "up":
					return "devices.status = 1 AND devices.disabled = 0", nil
				case "down":
					return "devices.status = 0 AND devices.disabled = 0", nil
				case "disabled":
					return "devices.disabled = 1", nil
				}
				return "", nil
			},
		},
	},
	EntityPorts: {
		table:    "ports",
		idColumn: "port_id",
		deleted:  "deleted",
		columns: []string{
			"port_id", "device_id", "port_label", "ifDescr", "ifName", "ifAlias",
			"ifIndex", "ifType", "ifSpeed", "ifHighSpeed", "ifMtu", "ifDuplex",
			"ifOperStatus", "ifAdminStatus", "ifPhysAddress", "ifVlan",
			// The rate columns are why this backend is worth having. Observium
			// computes them on every poll and stores them, so current
			// throughput is a column read rather than an RRD problem.
			"ifInOctets", "ifOutOctets", "ifInOctets_rate", "ifOutOctets_rate",
			"ifInOctets_perc", "ifOutOctets_perc",
			"ifInErrors", "ifOutErrors", "ifInErrors_rate", "ifOutErrors_rate",
			"ifInUcastPkts_rate", "ifOutUcastPkts_rate",
			"ifInDiscards_rate", "ifOutDiscards_rate",
			// Without these a caller cannot tell a rate of zero from a rate
			// last computed an hour ago.
			"poll_time", "poll_period", "ignore", "disabled",
		},
		filters: map[string]string{
			FilterDeviceID: "ports.device_id",
			FilterIfAlias:  "ports.ifAlias",
			FilterHostname: "devices.hostname",
			// ifOperStatus is an enum whose values are the API's words, so
			// this one needs no translation.
			FilterState: "ports.ifOperStatus",
		},
		clauses: map[string]func(string) (string, []any){
			// The API has an errors flag; the schema has counters. "Currently
			// reporting errors" is the rate being non-zero -- the cumulative
			// counter is non-zero for any interface that has ever had one.
			FilterErrors: func(string) (string, []any) {
				return "(ports.ifInErrors_rate > 0 OR ports.ifOutErrors_rate > 0)", nil
			},
			// An alerting interface is one alert_table has a failing row for.
			// There is no column on ports saying so, which is why this is a
			// subquery rather than a mapping.
			FilterAlerted: func(string) (string, []any) {
				return "ports.port_id IN (SELECT entity_id FROM alert_table " +
					"WHERE entity_type = 'port' AND alert_status = 0)", nil
			},
		},
		joinDevices: true,
	},
	EntitySensors: {
		table:    "sensors",
		idColumn: "sensor_id",
		deleted:  "sensor_deleted",
		columns: []string{
			"sensor_id", "device_id", "sensor_class", "sensor_type",
			"sensor_descr", "sensor_index", "sensor_unit", "sensor_value",
			"sensor_event", "sensor_status", "sensor_limit", "sensor_limit_warn",
			"sensor_limit_low", "sensor_limit_low_warn", "sensor_polled",
			"sensor_last_change", "sensor_ignore", "sensor_disable",
		},
		filters: map[string]string{
			FilterDeviceID: "device_id",
			FilterMetric:   "sensor_class",
			FilterEvent:    "sensor_event",
		},
		values: map[string]map[string]string{
			// The enum is ok/warning/alert/ignore. The API documents "warn",
			// and so did this tool's own schema hint, so the value a model was
			// told to send was one the column could never hold.
			FilterEvent: {"warn": "warning"},
		},
	},
	EntityAlerts: {
		table:    "alert_table",
		idColumn: "alert_table_id",
		columns: []string{
			"alert_table_id", "alert_test_id", "device_id", "entity_type",
			"entity_id", "alert_status", "last_message", "last_checked",
			"last_changed", "last_recovered", "last_ok", "last_failed",
			"has_alerted", "state", "count",
		},
		filters: map[string]string{FilterDeviceID: "device_id"},
		clauses: map[string]func(string) (string, []any){
			// Zero is the failing state, not one. Observium's own alerting
			// code writes alert_status = '0' beside last_message = 'Checks
			// failed', which is the only place the meaning is stated -- the
			// column is a bare tinyint and the API's word for it is "failed".
			// Getting this inverted would report a healthy estate as broken
			// and a broken one as fine.
			FilterStatus: func(v string) (string, []any) {
				switch strings.ToLower(v) {
				case "failed":
					return "alert_table.alert_status = 0", nil
				case "ok":
					return "alert_table.alert_status = 1", nil
				case "all":
					// A filter that matches everything, so that "all" is a
					// real answer rather than an unsupported one.
					return "1 = 1", nil
				}
				// delayed and suppressed are API states with no single column
				// behind them. Refused by name rather than guessed at.
				return "", nil
			},
		},
	},
	EntityAlertLog: {
		// The API calls this alert_log; the table is eventlog. Naming the
		// entity after the API keeps the tools identical across backends.
		table:    "eventlog",
		idColumn: "event_id",
		columns: []string{
			"event_id", "device_id", "timestamp", "message",
			"entity_type", "entity_id", "severity",
		},
		filters: map[string]string{FilterDeviceID: "device_id"},
		clauses: map[string]func(string) (string, []any){
			// The tools take unix timestamps, because that is what the API
			// documents. The column is a MySQL timestamp, so the conversion
			// happens here rather than asking a model to format a datetime.
			FilterFrom: func(v string) (string, []any) {
				return "eventlog.timestamp >= FROM_UNIXTIME(?)", []any{v}
			},
			FilterTo: func(v string) (string, []any) {
				return "eventlog.timestamp <= FROM_UNIXTIME(?)", []any{v}
			},
			FilterMessage: func(v string) (string, []any) {
				// The API filters the log by text in the message, which is a
				// substring match rather than an equality.
				return "eventlog.message LIKE ?", []any{"%" + v + "%"}
			},
		},
	},
	EntityStorage: {
		table:    "storage",
		idColumn: "storage_id",
		deleted:  "storage_deleted",
		columns: []string{
			"storage_id", "device_id", "storage_descr", "storage_type",
			"storage_size", "storage_used", "storage_free", "storage_perc",
			"storage_units", "storage_warn_limit", "storage_crit_limit",
			"storage_polled", "storage_ignore",
		},
		filters: map[string]string{FilterDeviceID: "device_id"},
	},
	EntityMempools: {
		table:    "mempools",
		idColumn: "mempool_id",
		deleted:  "mempool_deleted",
		columns: []string{
			"mempool_id", "device_id", "mempool_descr", "mempool_total",
			"mempool_used", "mempool_free", "mempool_perc",
			"mempool_warn_limit", "mempool_crit_limit", "mempool_polled",
			"mempool_ignore",
		},
		filters: map[string]string{FilterDeviceID: "device_id"},
	},
	EntityProcessors: {
		table:    "processors",
		idColumn: "processor_id",
		columns: []string{
			"processor_id", "device_id", "processor_descr", "processor_type",
			"processor_usage", "processor_warn_limit", "processor_crit_limit",
			"processor_polled", "processor_ignore",
		},
		filters: map[string]string{FilterDeviceID: "device_id"},
	},
	EntityInventory: {
		table:    "entPhysical",
		idColumn: "entPhysical_id",
		deleted:  "deleted",
		columns: []string{
			"entPhysical_id", "device_id", "entPhysicalDescr", "entPhysicalClass",
			"entPhysicalName", "entPhysicalModelName", "entPhysicalSerialNum",
			"entPhysicalHardwareRev", "entPhysicalFirmwareRev",
			"entPhysicalSoftwareRev", "entPhysicalMfgName", "entPhysicalAlias",
			"entPhysicalIsFRU", "ifIndex",
		},
		filters: map[string]string{
			FilterDeviceID: "device_id",
			FilterModel:    "entPhysicalModelName",
			FilterSerial:   "entPhysicalSerialNum",
		},
	},
	EntityNeighbours: {
		table:    "neighbours",
		idColumn: "neighbour_id",
		columns: []string{
			"neighbour_id", "device_id", "port_id", "remote_port_id", "active",
			"protocol", "remote_hostname", "remote_port", "remote_platform",
			"remote_version", "remote_address",
		},
		filters: map[string]string{FilterDeviceID: "device_id"},
	},
	EntityAddresses: {
		// The API serves v4 and v6 under one key. Two tables, unioned in
		// Read, because a UNION here would have to reconcile column names that
		// differ for a reason.
		table:    "ipv4_addresses",
		idColumn: "ipv4_address_id",
		columns: []string{
			"ipv4_address_id", "device_id", "ipv4_address", "ipv4_prefixlen",
			"port_id", "ifIndex",
		},
		filters: map[string]string{FilterDeviceID: "device_id"},
	},
	EntityVLANs: {
		table:    "vlans",
		idColumn: "vlan_id",
		columns: []string{
			"vlan_id", "device_id", "ifIndex", "vlan_vlan", "vlan_domain",
			"vlan_name", "vlan_type", "vlan_mtu", "vlan_status",
		},
		filters: map[string]string{FilterDeviceID: "device_id"},
	},
}

// ipv6Query is the second half of EntityAddresses.
var ipv6Query = entityQuery{
	table:    "ipv6_addresses",
	idColumn: "ipv6_address_id",
	columns: []string{
		"ipv6_address_id", "device_id", "ipv6_address", "ipv6_compressed",
		"ipv6_prefixlen", "ipv6_origin", "port_id", "ifIndex",
	},
	filters: map[string]string{FilterDeviceID: "device_id"},
}

// newMySQLReader connects and proves the connection is read-only.
func newMySQLReader(cfg Config, password string, log *slog.Logger,
	now func() time.Time, cache *readCache,
	observe func(string, time.Duration)) (*mysqlReader, error) {

	dsn := mysql.NewConfig()
	dsn.Net = "tcp"
	dsn.Addr = net.JoinHostPort(cfg.DBHost, strconv.Itoa(cfg.DBPort))
	dsn.DBName = cfg.DBName
	dsn.User = cfg.DBUser
	dsn.Passwd = password
	dsn.Timeout = cfg.Timeout
	dsn.ReadTimeout = cfg.Timeout
	// Observium stores timestamps as DATETIME in the server's zone. Parsing
	// them into time.Time rather than handing back a byte slice is what lets a
	// tool result carry an RFC 3339 string a model can reason about.
	dsn.ParseTime = true
	dsn.Loc = time.UTC
	// Interpolation off: every query below is parameterised, and leaving this
	// on would mean the driver building SQL strings from caller input.
	dsn.InterpolateParams = false

	db, err := sql.Open("mysql", dsn.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("observium: opening the database: %w", err)
	}
	// Modest, because this is somebody's monitoring database and mcpd is not
	// the only thing using it. The poller matters more than we do.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)

	return &mysqlReader{
		db:      db,
		cfg:     cfg,
		log:     log,
		limiter: rate.NewLimiter(rate.Limit(cfg.RequestsPerSecond), 1),
		cache:   cache,
		observe: observe,
		now:     now,
	}, nil
}

func (r *mysqlReader) Close() error { return r.db.Close() }

func (r *mysqlReader) Describe() string {
	return fmt.Sprintf("database %s on %s, restricted to SELECT by its own grants",
		r.cfg.DBName, net.JoinHostPort(r.cfg.DBHost, strconv.Itoa(r.cfg.DBPort)))
}

// Probe connects, proves the account cannot write, and proves the schema is
// the one these queries were written against.
func (r *mysqlReader) Probe(ctx context.Context) error {
	if err := r.db.PingContext(ctx); err != nil {
		return fmt.Errorf("observium: cannot reach the database at %s: %w",
			net.JoinHostPort(r.cfg.DBHost, strconv.Itoa(r.cfg.DBPort)), err)
	}
	if err := r.checkGrants(ctx); err != nil {
		return err
	}
	return r.checkSchema(ctx)
}

// writeGrants are the privileges that would let this account change something.
//
// Matched by name rather than by trying to prove the negative: MySQL's grant
// vocabulary is long and growing, so a list of what is *allowed* would call a
// privilege added in a later release safe by default. This list errs the other
// way -- an unrecognised privilege is not on it, so it does not trip the
// check, which is why ALL PRIVILEGES is matched separately and first.
var writeGrants = []string{
	"ALL PRIVILEGES", "INSERT", "UPDATE", "DELETE", "DROP", "ALTER",
	"CREATE", "TRUNCATE", "REPLACE", "SUPER", "FILE",
	"PROCESS", "RELOAD", "SHUTDOWN", "LOCK TABLES", "REFERENCES",
	"EXECUTE", "EVENT", "TRIGGER", "INDEX",
}

// grantIsWrite reports whether one line of SHOW GRANTS gives the account a way
// to change something.
//
// Only the privilege list matters and it ends at ON, because everything after
// that is a database name and a host -- and a schema called `create_backup` or
// a host called insert.example.com would otherwise refuse a perfectly
// restricted account. A false refusal here is one nobody can debug from the
// message, so the narrowing is deliberate.
//
// WITH GRANT OPTION is the exception and is checked against the whole line: it
// is written after the ON clause, so truncating first would miss the one
// privilege that lets an account give itself the others.
func grantIsWrite(grant string) bool {
	upper := strings.ToUpper(grant)
	if strings.Contains(upper, "WITH GRANT OPTION") {
		return true
	}
	privileges := upper
	if i := strings.Index(upper, " ON "); i > 0 {
		privileges = upper[:i]
	}
	for _, bad := range writeGrants {
		if strings.Contains(privileges, bad) {
			return true
		}
	}
	return false
}

// checkGrants refuses an account that can write.
//
// This is the read-only guarantee for this backend. The API backend proves it
// by refusing a non-GET at the transport; there is no transport here, so it is
// proved by the database server instead -- which is stronger, because a bug in
// this package cannot widen a grant.
//
// It runs at startup rather than per query. A grant changed underneath us is
// not something this can notice, and a check on every read would be a round
// trip per read to defend against an operator deliberately widening access to
// their own database.
func (r *mysqlReader) checkGrants(ctx context.Context) error {
	rows, err := r.db.QueryContext(ctx, "SHOW GRANTS FOR CURRENT_USER()")
	if err != nil {
		// Some managed MySQL services refuse SHOW GRANTS to unprivileged
		// accounts. Refusing to start would be wrong -- the account may well
		// be correctly restricted -- but so would saying nothing, because the
		// guarantee this function exists to provide is then absent.
		r.log.Warn("could not read the database account's grants, so mcpd "+
			"cannot confirm it is read-only; make sure it has SELECT and "+
			"nothing else", "error", err)
		return nil
	}
	defer rows.Close()

	for rows.Next() {
		var grant string
		if err := rows.Scan(&grant); err != nil {
			return fmt.Errorf("observium: reading grants: %w", err)
		}
		if grantIsWrite(grant) {
			return fmt.Errorf("observium: the database account %q can change "+
				"the monitoring database, and this integration will not connect "+
				"with one that can. Grant it SELECT only: "+
				"GRANT SELECT ON %s.* TO '%s'@'...'",
				r.cfg.DBUser, r.cfg.DBName, r.cfg.DBUser)
		}
	}
	return rows.Err()
}

// checkSchema proves the columns these queries name actually exist.
//
// Observium versions its API and not its schema. A column that moved between
// releases would otherwise produce a query error inside the first tool call an
// assistant makes, at which point the message is a MySQL error about a table
// nobody reading it has heard of. Named here, at startup, it is a sentence
// somebody can act on.
func (r *mysqlReader) checkSchema(ctx context.Context) error {
	queries := make([]entityQuery, 0, len(schema)+1)
	for _, q := range schema {
		queries = append(queries, q)
	}
	queries = append(queries, ipv6Query)

	for _, q := range queries {
		rows, err := r.db.QueryContext(ctx,
			`SELECT column_name FROM information_schema.columns
			 WHERE table_schema = ? AND table_name = ?`, r.cfg.DBName, q.table)
		if err != nil {
			return fmt.Errorf("observium: inspecting table %s: %w", q.table, err)
		}
		present := map[string]bool{}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				return fmt.Errorf("observium: inspecting table %s: %w", q.table, err)
			}
			present[strings.ToLower(name)] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return fmt.Errorf("observium: inspecting table %s: %w", q.table, err)
		}
		if len(present) == 0 {
			return fmt.Errorf("observium: the database %q has no table %q. Is "+
				"this an Observium database?", r.cfg.DBName, q.table)
		}
		var missing []string
		for _, col := range q.columns {
			if !present[strings.ToLower(col)] {
				missing = append(missing, col)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("observium: table %s is missing %s. This "+
				"Observium's schema differs from the one these queries were "+
				"written against, and reading it anyway would return wrong "+
				"answers rather than no answer", q.table, strings.Join(missing, ", "))
		}
	}
	return nil
}

// Read answers a query by building parameterised SQL.
//
// Every value reaches MySQL as a bound parameter and every identifier comes
// from the schema map above, which is a compile-time constant. Nothing a
// caller supplies is ever concatenated into the statement -- a filter name
// that is not in the map is dropped rather than trusted.
func (r *mysqlReader) Read(ctx context.Context, entity Entity, filters url.Values, limit int) (Page, error) {
	if r.cache == nil {
		return r.read(ctx, entity, filters, limit)
	}
	got, err := r.cache.reuse(ctx, "/"+string(entity), filters, func(ctx context.Context) (any, error) {
		return r.read(ctx, entity, filters, limit)
	})
	if err != nil {
		return Page{}, err
	}
	page, ok := got.(Page)
	if !ok {
		return Page{}, fmt.Errorf("observium: cached value for %s was not a page", entity)
	}
	return page, nil
}

func (r *mysqlReader) read(ctx context.Context, entity Entity, filters url.Values, limit int) (Page, error) {
	q, ok := schema[entity]
	if !ok {
		return Page{}, fmt.Errorf("observium: no database query for %s", entity)
	}
	if limit <= 0 || limit > r.cfg.MaxItems {
		limit = r.cfg.MaxItems
	}

	page, err := r.selectFrom(ctx, q, filters, limit)
	if err != nil {
		return Page{}, err
	}
	// Addresses are one API key over two tables. The second half is fetched
	// under whatever headroom the first left, so the pair still honours one
	// ceiling rather than two.
	if entity == EntityAddresses && len(page.Items) < limit {
		v6, err := r.selectFrom(ctx, ipv6Query, filters, limit-len(page.Items))
		if err != nil {
			return Page{}, err
		}
		page.Items = append(page.Items, v6.Items...)
		page.Total += v6.Total
		page.Truncated = page.Truncated || v6.Truncated
	}
	return page, nil
}

func (r *mysqlReader) selectFrom(ctx context.Context, q entityQuery, filters url.Values, limit int) (Page, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return Page{}, fmt.Errorf("observium: waiting to query %s: %w", q.table, err)
	}

	var where []string
	var args []any

	// Observium soft-deletes. A port pulled out of a switch stays in the table
	// with deleted=1, so a query that ignores this reports interfaces that no
	// longer exist as though they were live.
	if q.deleted != "" {
		where = append(where, q.table+"."+q.deleted+" = 0")
	}

	for name, values := range filters {
		if len(values) == 0 || strings.TrimSpace(values[0]) == "" {
			continue
		}
		value := values[0]

		if name == FilterID {
			where = append(where, q.table+"."+q.idColumn+" = ?")
			args = append(args, value)
			continue
		}
		// An option shapes the answer rather than narrowing it, so a backend
		// that cannot honour one has still answered the right question.
		if outputOptions[name] {
			continue
		}

		// A filter that is not an equality against one column.
		if clause, ok := q.clauses[name]; ok {
			sql, extra := clause(value)
			if sql == "" {
				return Page{}, fmt.Errorf("observium: %q is not a value %s "+
					"accepts for %s", value, name, q.table)
			}
			where = append(where, sql)
			args = append(args, extra...)
			continue
		}

		column, ok := q.filters[name]
		if !ok {
			// Refused, not dropped.
			//
			// This used to continue, on the reasoning that a filter which does
			// not apply gives a narrower answer rather than a wrong one. That
			// is backwards. Dropping "status = down" does not narrow anything
			// -- it returns every device, presented as though it had been
			// filtered, and a model asked which devices are down then reports
			// all of them. A filter nobody can apply has to be an error.
			return Page{}, fmt.Errorf("observium: reading %s from the database "+
				"cannot filter by %q, and answering without that filter would "+
				"return everything as though it matched", q.table, name)
		}
		if translated, ok := q.values[name][strings.ToLower(value)]; ok {
			value = translated
		}
		if !strings.Contains(column, ".") {
			column = q.table + "." + column
		}
		where = append(where, column+" = ?")
		args = append(args, value)
	}

	columns := make([]string, len(q.columns))
	for i, c := range q.columns {
		columns[i] = q.table + ".`" + c + "`"
	}

	from := q.table
	if q.joinDevices {
		from += " JOIN devices ON devices.device_id = " + q.table + ".device_id"
	}

	stmt := "SELECT " + strings.Join(columns, ", ") + " FROM " + from
	if len(where) > 0 {
		stmt += " WHERE " + strings.Join(where, " AND ")
	}
	// Ordered by primary key for the same reason the API backend sorts its
	// keyed object: an unordered answer makes two identical calls look like a
	// change happened.
	stmt += " ORDER BY " + q.table + "." + q.idColumn
	// One more than asked for, so a full page can be distinguished from a page
	// that happens to end exactly at the ceiling.
	stmt += " LIMIT " + strconv.Itoa(limit+1)

	started := r.now()
	rows, err := r.db.QueryContext(ctx, stmt, args...)
	elapsed := r.now().Sub(started)
	if err != nil {
		r.observe("error", elapsed)
		return Page{}, fmt.Errorf("observium: querying %s: %w", q.table, err)
	}
	defer rows.Close()

	page, err := scanRows(rows, q.columns, limit)
	if err != nil {
		r.observe("error", elapsed)
		return Page{}, fmt.Errorf("observium: reading %s: %w", q.table, err)
	}
	r.observe("ok", elapsed)

	// Counted separately only when it would tell the caller something. A page
	// that was not truncated already knows its own total.
	if page.Truncated {
		if total, err := r.count(ctx, q, where, args); err == nil {
			page.Total = total
		}
	} else {
		page.Total = len(page.Items)
	}
	return page, nil
}

// count answers how many rows the filter actually matched, so a truncated page
// can say what it is a part of.
//
// Best effort: a failure here loses the total and keeps the answer, because a
// page of real data with an unknown total is worth more than an error.
func (r *mysqlReader) count(ctx context.Context, q entityQuery, where []string, args []any) (int, error) {
	from := q.table
	if q.joinDevices {
		from += " JOIN devices ON devices.device_id = " + q.table + ".device_id"
	}
	stmt := "SELECT COUNT(*) FROM " + from
	if len(where) > 0 {
		stmt += " WHERE " + strings.Join(where, " AND ")
	}
	var n int
	if err := r.db.QueryRowContext(ctx, stmt, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// scanRows turns a result set into the same shape the API backend produces, so
// a tool cannot tell which backend answered.
func scanRows(rows *sql.Rows, columns []string, limit int) (Page, error) {
	var page Page

	for rows.Next() {
		if len(page.Items) >= limit {
			// The extra row from LIMIT n+1 exists only to prove there was more.
			page.Truncated = true
			break
		}
		cells := make([]any, len(columns))
		for i := range cells {
			cells[i] = new(any)
		}
		if err := rows.Scan(cells...); err != nil {
			return Page{}, err
		}
		item := make(map[string]any, len(columns))
		for i, name := range columns {
			item[name] = normalise(*(cells[i].(*any)))
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}
	return page, nil
}

// normalise turns a driver value into something that survives JSON.
//
// MySQL hands back []byte for text and for anything it is unsure of, and a
// []byte marshals to base64 -- so a hostname would reach the model as
// "cm91dGVyLTEubG9jYWw=" unless it is converted here. Numbers stay numbers,
// which matters because a model asked whether a disk is over 90 per cent
// should be comparing against a number rather than a string.
func normalise(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case []byte:
		s := string(t)
		// Observium stores several numeric columns as strings. Returning them
		// as numbers where they plainly are numbers is the difference between
		// a model comparing values and a model comparing text.
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f
		}
		return s
	case time.Time:
		return t.UTC().Format(time.RFC3339)
	default:
		return v
	}
}
