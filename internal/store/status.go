package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// LatestSchemaVersion returns the most recent migration version applied,
// or empty string if none.
func (s *Store) LatestSchemaVersion(ctx context.Context) (string, error) {
	var v string
	err := s.Pool.QueryRow(ctx, `
		SELECT version FROM schema_migrations ORDER BY applied_at DESC LIMIT 1
	`).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// GinPresent reports whether the gin steward account exists. The bootstrap
// invariant from CLAUDE.md is that gin must be present once the platform is
// "initialized"; the UI uses this to decide between first-time setup and
// the regular login screen.
func (s *Store) GinPresent(ctx context.Context) (bool, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE username = 'gin'`).Scan(&n)
	return n > 0, err
}
