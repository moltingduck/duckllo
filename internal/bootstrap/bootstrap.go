// Package bootstrap creates the gin steward account and any other
// startup-time invariants required by CLAUDE.md.
package bootstrap

import (
	"context"
	"errors"
	"log"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/moltingduck/duckllo/internal/auth"
	"github.com/moltingduck/duckllo/internal/store"
)

// Run is idempotent. It checks whether the gin user exists; if not and
// DUCKLLO_GIN_PASSWORD is set, it creates the account with system_role=admin
// so the rest of the platform can rely on its presence.
func Run(ctx context.Context, pool *pgxpool.Pool) error {
	st := store.New(pool)
	_, err := st.UserByUsername(ctx, "gin")
	if err == nil {
		return nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return err
	}

	pw := os.Getenv("DUCKLLO_GIN_PASSWORD")
	if pw == "" {
		log.Printf("bootstrap: gin user missing and DUCKLLO_GIN_PASSWORD unset — first /api/auth/register call will become the admin")
		return nil
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	if _, err := st.CreateUser(ctx, "gin", hash, "Gin (steward)", "admin"); err != nil {
		return err
	}
	log.Printf("bootstrap: created gin admin user")
	return nil
}
