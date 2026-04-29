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
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/moltingduck/duckllo/internal/bootstrap"
	"github.com/moltingduck/duckllo/internal/config"
	"github.com/moltingduck/duckllo/internal/db"
	"github.com/moltingduck/duckllo/internal/dotenv"
	httpapi "github.com/moltingduck/duckllo/internal/http"
	"github.com/moltingduck/duckllo/internal/selfhost"
)

func main() {
	if path, err := dotenv.LoadDefault(); err != nil {
		fmt.Fprintln(os.Stderr, "warning: dotenv load:", err)
	} else if path != "" {
		fmt.Fprintln(os.Stderr, "loaded env from", path)
	}

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
	case "selfhost":
		if err := runSelfhost(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "selfhost:", err)
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
	fmt.Fprintln(os.Stderr, "usage: duckllo {serve|migrate|selfhost}")
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

// runSelfhost wires up the dogfood loop: ensure gin, ensure a 'duckllo'
// project, mint an API key, seed harness rules from the codified
// conventions, write .duckllo.env. Idempotent on re-run — only the
// freshly-minted key triggers an env-file write because the plaintext
// is unrecoverable on subsequent invocations.
func runSelfhost(args []string) error {
	fs := flag.NewFlagSet("selfhost", flag.ExitOnError)
	projectName := fs.String("project-name", "duckllo", "project name to ensure")
	keyLabel := fs.String("key-label", "selfhost-runner", "label for the minted API key")
	envFile := fs.String("env-file", ".duckllo.env", "path to the env file to write")
	baseURL := fs.String("base-url", env("DUCKLLO_URL", "http://localhost:3000"), "duckllo URL recorded in the env file")
	force := fs.Bool("force", false, "overwrite an existing env file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if err := bootstrap.Run(ctx, pool); err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}

	res, err := selfhost.Run(ctx, pool, selfhost.Options{
		ProjectName: *projectName,
		APIKeyLabel: *keyLabel,
		EnvFile:     *envFile,
		BaseURL:     *baseURL,
		Force:       *force,
	})
	if err != nil {
		return err
	}

	fmt.Println("duckllo selfhost — done")
	fmt.Printf("  project   %s   id=%s\n", res.ProjectName, res.ProjectID)
	fmt.Printf("  rules     %d added, %d already present\n", res.RulesAdded, res.RulesSkipped)
	if res.APIKey != "" {
		fmt.Printf("  api key   %s   label=%s   (plaintext shown once)\n", res.APIKeyPrefix+"_…", res.APIKeyLabel)
	} else {
		fmt.Printf("  api key   %s   label=%s   (already exists; plaintext unrecoverable)\n",
			res.APIKeyPrefix+"_…", res.APIKeyLabel)
	}
	switch {
	case res.EnvWritten:
		fmt.Printf("  env file  %s   written\n", *envFile)
	case res.EnvSkipped && res.APIKey == "":
		fmt.Printf("  env file  %s   left alone (key already minted; if you need to rotate, revoke in the UI and re-run with --force)\n", *envFile)
	case res.EnvSkipped:
		fmt.Printf("  env file  %s   already exists; skipping (re-run with --force to overwrite)\n", *envFile)
	}
	fmt.Println()
	fmt.Println("next: bring up the server (`make serve`) and the runner (`make runner`).")
	return nil
}

// env mirrors the helper used by cmd/runner so the same fallback logic
// works everywhere.
func env(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
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
