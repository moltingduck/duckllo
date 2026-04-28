// Command duckllo is the harness-engineering coordination plane.
//
// Subcommands:
//
//	duckllo serve     start the HTTP server
//	duckllo migrate   apply pending database migrations and exit
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/moltingduck/duckllo/internal/bootstrap"
	"github.com/moltingduck/duckllo/internal/config"
	"github.com/moltingduck/duckllo/internal/db"
	httpapi "github.com/moltingduck/duckllo/internal/http"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "serve":
		if err := runServe(); err != nil {
			fmt.Fprintln(os.Stderr, "serve:", err)
			os.Exit(1)
		}
	case "migrate":
		if err := runMigrate(); err != nil {
			fmt.Fprintln(os.Stderr, "migrate:", err)
			os.Exit(1)
		}
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: duckllo {serve|migrate}")
}

func runMigrate() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	pool, err := db.Open(context.Background(), cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	return db.Migrate(context.Background(), pool)
}

func runServe() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("auto-migrate: %w", err)
	}
	if err := bootstrap.Run(ctx, pool); err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}

	srv := httpapi.NewServer(cfg, pool)
	if err := srv.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
