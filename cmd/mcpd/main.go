// Command mcpd is an extensible host for Model Context Protocol integrations.
//
// It serves one MCP endpoint per plugin, gates every infrastructure mutation
// behind a durable approval workflow, and keeps SQLite as the sole authority
// for whether a change was authorised.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spoked/mcpd/internal/app"
	"github.com/spoked/mcpd/internal/config"
	"github.com/spoked/mcpd/internal/observability"
)

func main() {
	if err := run(); err != nil {
		// The logger may not exist yet if configuration failed, so report to
		// stderr directly and let the exit code carry the failure.
		fmt.Fprintln(os.Stderr, "mcpd:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		configPath  = flag.String("config", "/etc/mcpd/config.yaml", "path to the configuration file")
		envPath     = flag.String("env", "", "path to a .env file (default: .env beside the config, if present)")
		showVersion = flag.Bool("version", false, "print the version and exit")
		checkOnly   = flag.Bool("check", false, "validate the configuration and exit")
		initDir     = flag.String("init", "", "generate a config, token and directories under this path, then exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("mcpd", app.Version)
		return nil
	}
	if *initDir != "" {
		return initialize(*initDir)
	}

	// A .env is loaded before configuration so that secret references in the
	// config resolve against it. Real environment variables always win, so an
	// orchestrator injecting a rotated secret is never overridden by a stale
	// file. A missing .env is normal: systemd passes credentials through
	// LoadCredential instead.
	if err := config.LoadEnvFile(resolveEnvPath(*envPath, *configPath)); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	log := observability.NewLogger(os.Stdout,
		observability.ParseLevel(cfg.Logging.Level), cfg.Logging.Format)

	if *checkOnly {
		for _, w := range cfg.Warnings() {
			fmt.Fprintln(os.Stderr, "warning:", w)
		}
		fmt.Println("configuration is valid")
		return nil
	}

	log.Info("starting mcpd", "version", app.Version, "config", *configPath)

	// SIGINT and SIGTERM both begin a graceful drain. A second signal is left
	// to the default handler, so an operator can always force an exit.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	application, err := app.New(ctx, cfg, log)
	if err != nil {
		return err
	}

	if err := application.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	log.Info("mcpd stopped")
	return nil
}

// resolveEnvPath decides which .env to read.
//
// An explicit --env always wins. Otherwise the file is looked for beside the
// config, which keeps a deployment's configuration and its secrets together,
// and then in the working directory for the common `mcpd -config ./x.yaml`
// case during development.
func resolveEnvPath(explicit, configPath string) string {
	if explicit != "" {
		return explicit
	}
	beside := filepath.Join(filepath.Dir(configPath), config.DefaultEnvFile)
	if _, err := os.Stat(beside); err == nil {
		return beside
	}
	return config.DefaultEnvFile
}
