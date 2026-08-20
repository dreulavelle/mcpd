package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spoked/mcpd/internal/config"
	"github.com/spoked/mcpd/internal/storage/sqlite"
)

// backup writes a consistent copy of the database while mcpd is running.
//
// Copying the file directly is not safe: under WAL the committed state is
// split across the database and the -wal file, so a plain `cp` can capture a
// torn snapshot that restores to a state no transaction ever produced. SQLite's
// VACUUM INTO takes a consistent snapshot without blocking writers, which
// matters because this holds the approval history and the audit trail.
func backup(configPath, envPath, destination string) error {
	if err := config.LoadEnvFile(resolveEnvPath(envPath, configPath)); err != nil {
		return err
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	dest, err := filepath.Abs(destination)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", destination, err)
	}
	// A directory destination gets a timestamped filename, so a cron entry
	// pointed at a directory does not overwrite yesterday's copy.
	if info, statErr := os.Stat(dest); statErr == nil && info.IsDir() {
		dest = filepath.Join(dest,
			fmt.Sprintf("mcpd-%s.db", time.Now().UTC().Format("20060102T150405Z")))
	}
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("%s already exists; refusing to overwrite a backup", dest)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	db, err := sqlite.Open(ctx, sqlite.Options{
		Path:        cfg.Storage.Path,
		BusyTimeout: cfg.Storage.BusyTimeout,
	})
	if err != nil {
		return err
	}
	defer db.Close()

	if err := db.Backup(ctx, dest); err != nil {
		return err
	}

	info, err := os.Stat(dest)
	if err != nil {
		return err
	}
	fmt.Printf("wrote %s (%.1f MiB)\n", dest, float64(info.Size())/(1<<20))
	return nil
}
