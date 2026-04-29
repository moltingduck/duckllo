package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/moltingduck/duckllo/internal/models"
)

var ErrNotFound = errors.New("not found")

// ErrSpecNotEnqueueable is returned by EnqueueRun when the spec isn't in
// 'approved' status — typically because it's still draft, was rejected,
// or already has a run in flight that drove it to 'running'/'validated'.
// Callers map this to HTTP 400 so the user gets a clear error.
var ErrSpecNotEnqueueable = errors.New("spec is not in 'approved' status")

func (s *Store) CreateUser(ctx context.Context, username, passwordHash, displayName, role string) (*models.User, error) {
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO users (username, password_hash, display_name, system_role)
		VALUES ($1, $2, $3, $4)
		RETURNING id, username, password_hash, display_name, system_role, disabled, created_at
	`, username, passwordHash, displayName, role)

	var u models.User
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.DisplayName, &u.SystemRole, &u.Disabled, &u.CreatedAt); err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *Store) UserByUsername(ctx context.Context, username string) (*models.User, error) {
	var u models.User
	err := s.Pool.QueryRow(ctx, `
		SELECT id, username, password_hash, COALESCE(display_name,''), system_role, disabled, created_at
		FROM users WHERE username = $1
	`, username).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.DisplayName, &u.SystemRole, &u.Disabled, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *Store) UserByID(ctx context.Context, id uuid.UUID) (*models.User, error) {
	var u models.User
	err := s.Pool.QueryRow(ctx, `
		SELECT id, username, password_hash, COALESCE(display_name,''), system_role, disabled, created_at
		FROM users WHERE id = $1
	`, id).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.DisplayName, &u.SystemRole, &u.Disabled, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (s *Store) SearchUsers(ctx context.Context, q string, limit int) ([]models.User, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, username, '', COALESCE(display_name,''), system_role, disabled, created_at
		FROM users
		WHERE disabled = FALSE AND (username ILIKE $1 OR display_name ILIKE $1)
		ORDER BY username
		LIMIT $2
	`, "%"+q+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []models.User{}
	for rows.Next() {
		var u models.User
		if err := rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.DisplayName, &u.SystemRole, &u.Disabled, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
