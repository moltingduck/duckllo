// Package config loads runtime configuration from environment variables and
// flag overrides. Defaults are tuned for `docker compose up` development.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	// HTTP listen address, e.g. ":3000".
	Addr string

	// Postgres connection URL (libpq DSN or postgres:// URL).
	DatabaseURL string

	// Path on disk where uploads (artifacts, transcripts) are stored.
	UploadsDir string

	// Anthropic API key used by the runner. Server reads it only to validate
	// presence at startup so misconfiguration surfaces immediately.
	AnthropicAPIKey string

	// Tailscale preauth key (Phase 2). Loaded but unused in Phase 1.
	TailscalePreauthKey string

	// Container runtime selector ("docker" | "podman"). Phase 2.
	ContainerRuntime string

	// Maximum size of an uploaded artifact in bytes.
	MaxUploadBytes int64
}

func Load() (*Config, error) {
	cfg := &Config{
		Addr:                env("DUCKLLO_ADDR", ":3000"),
		DatabaseURL:         env("DATABASE_URL", "postgres://duckllo:duckllo@localhost:5432/duckllo?sslmode=disable"),
		UploadsDir:          env("DUCKLLO_UPLOADS", "uploads"),
		AnthropicAPIKey:     os.Getenv("ANTHROPIC_API_KEY"),
		TailscalePreauthKey: os.Getenv("TAILSCALE_PREAUTH_KEY"),
		ContainerRuntime:    env("CONTAINER_RUNTIME", "docker"),
	}

	max, err := strconv.ParseInt(env("DUCKLLO_MAX_UPLOAD", "33554432"), 10, 64) // 32 MiB
	if err != nil {
		return nil, fmt.Errorf("DUCKLLO_MAX_UPLOAD: %w", err)
	}
	cfg.MaxUploadBytes = max

	if cfg.DatabaseURL == "" {
		return nil, errors.New("DATABASE_URL is required")
	}
	return cfg, nil
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
