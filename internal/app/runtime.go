package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/spoked/mcpd/internal/config"
	"github.com/spoked/mcpd/internal/settings"
	"github.com/spoked/mcpd/internal/storage/sqlite"
)

// startup holds the moved settings that are read once, because what they
// configure is built once.
//
// Every field here is declared ApplyRestart in the schema, and the two facts
// are meant to stay together: a value that is snapshotted and a control that
// says "needs a restart" are the same claim, and the dashboard is only honest
// while they agree.
type startup struct {
	publicURL         string
	tlsSelfSigned     bool
	frontendEnabled   bool
	readHeaderTimeout time.Duration
	readTimeout       time.Duration
	writeTimeout      time.Duration
	idleTimeout       time.Duration
	relaxedDurability bool
}

// resolveStartup reads the settings the graph is about to be built from.
func resolveStartup(ctx context.Context, store *settings.Store) startup {
	return startup{
		publicURL:         store.FieldString(ctx, settings.KeyServerPublicURL),
		tlsSelfSigned:     store.FieldString(ctx, settings.KeyServerTLSMode) == "self-signed",
		frontendEnabled:   store.FieldBool(ctx, settings.KeyServerFrontendEnabled),
		readHeaderTimeout: store.FieldDuration(ctx, settings.KeyServerReadHeaderTimeout),
		readTimeout:       store.FieldDuration(ctx, settings.KeyServerReadTimeout),
		writeTimeout:      store.FieldDuration(ctx, settings.KeyServerWriteTimeout),
		idleTimeout:       store.FieldDuration(ctx, settings.KeyServerIdleTimeout),
		relaxedDurability: store.FieldBool(ctx, settings.KeyStorageRelaxedDurability),
	}
}

// The live reads. Each is a single call site, so a value changed in the
// dashboard reaches the thing that uses it on the next request rather than on
// the next restart -- which is what the schema promises for these keys.

// publicURL is the MCP endpoint as it looks from outside.
func (a *App) publicURL(ctx context.Context) string {
	return a.settings.FieldString(ctx, settings.KeyServerPublicURL)
}

// frontendPublicURL is how a browser reaches the dashboard, when something in
// front of this process terminates TLS for it.
func (a *App) frontendPublicURL(ctx context.Context) string {
	return a.settings.FieldString(ctx, settings.KeyServerFrontendPublicURL)
}

// sessionTTL bounds a browser signed in from now on.
func (a *App) sessionTTL(ctx context.Context) time.Duration {
	return a.settings.FieldDuration(ctx, settings.KeyAccountsSessionTTL)
}

// shutdownTimeout is read when mcpd is asked to stop rather than when it
// starts, which is what lets it be live.
func (a *App) shutdownTimeout(ctx context.Context) time.Duration {
	return a.settings.FieldDuration(ctx, settings.KeyServerShutdownTimeout)
}

// openStorage opens the database with the settings it holds about itself.
//
// The circularity is real and there is no way around it: how the pools are
// opened is stored inside the pools. It is resolved by opening once with the
// defaults, reading the two values, and reopening only if they differ -- which
// costs a few milliseconds on a host that has customised them and nothing at
// all on one that has not.
//
// Before the store has anything, the startup file's values are used. That is
// the same one turn the file gets everywhere else: it seeds, and the import
// that runs a moment later writes what it seeded into the store.
func openStorage(ctx context.Context, cfg *config.Config, log *slog.Logger) (*sqlite.DB, error) {
	legacy := cfg.Legacy()

	busy := settings.DefaultDuration(settings.KeyStorageBusyTimeout)
	if legacy.Storage.BusyTimeout != nil {
		busy = *legacy.Storage.BusyTimeout
	}
	relaxed := settings.DefaultBool(settings.KeyStorageRelaxedDurability)
	if legacy.Storage.RelaxedDurability != nil {
		relaxed = *legacy.Storage.RelaxedDurability
	}

	opts := sqlite.Options{
		Path:              cfg.Storage.Path,
		ReadPoolSize:      cfg.Storage.ReadPoolSize,
		BusyTimeout:       busy,
		RelaxedDurability: relaxed,
	}
	db, err := sqlite.Open(ctx, opts)
	if err != nil {
		return nil, err
	}

	stored := peekStorageSettings(ctx, db)
	if stored.busy != nil {
		busy = *stored.busy
	}
	if stored.relaxed != nil {
		relaxed = *stored.relaxed
	}
	if busy == opts.BusyTimeout && relaxed == opts.RelaxedDurability {
		return db, nil
	}

	log.Info("reopening the database with the settings it holds about itself",
		"busy_timeout", busy, "relaxed_durability", relaxed)
	db.Close()
	opts.BusyTimeout, opts.RelaxedDurability = busy, relaxed
	return sqlite.Open(ctx, opts)
}

type storagePragmas struct {
	busy    *time.Duration
	relaxed *bool
}

// peekStorageSettings reads the two pool settings straight out of the table.
//
// Straight out, rather than through the settings store, because the store is
// built from a database that is already open and this decides how to open it.
// Anything that goes wrong -- a database being created for the first time, a
// schema older than the settings table -- means there is nothing stored, which
// is the answer rather than an error.
func peekStorageSettings(ctx context.Context, db *sqlite.DB) storagePragmas {
	var out storagePragmas

	rows, err := db.Reader().QueryContext(ctx,
		`SELECT key, value FROM settings WHERE key IN (?, ?)`,
		settings.KeyStorageBusyTimeout, settings.KeyStorageRelaxedDurability)
	if err != nil {
		return out
	}
	defer rows.Close()

	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return storagePragmas{}
		}
		switch key {
		case settings.KeyStorageBusyTimeout:
			var seconds int
			if err := json.Unmarshal([]byte(value), &seconds); err == nil && seconds > 0 {
				d := time.Duration(seconds) * time.Second
				out.busy = &d
			}
		case settings.KeyStorageRelaxedDurability:
			var b bool
			if err := json.Unmarshal([]byte(value), &b); err == nil {
				out.relaxed = &b
			}
		}
	}
	return out
}
