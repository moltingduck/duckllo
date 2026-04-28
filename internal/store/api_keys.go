package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/moltingduck/duckllo/internal/models"
)

func (s *Store) CreateAPIKey(ctx context.Context, userID, projectID uuid.UUID, label, prefix, hash string, perms []byte) (*models.APIKey, error) {
	var k models.APIKey
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO api_keys (key_prefix, key_hash, label, user_id, project_id, permissions)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, key_prefix, COALESCE(label,''), user_id, project_id, permissions, last_used_at, created_at
	`, prefix, hash, label, userID, projectID, perms).Scan(
		&k.ID, &k.KeyPrefix, &k.Label, &k.UserID, &k.ProjectID, &k.Permissions, &k.LastUsedAt, &k.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &k, nil
}

// FindAPIKeysByPrefix returns the candidate rows that share a prefix; the
// caller must bcrypt-compare each hash against the plaintext token. The
// prefix is indexed so this stays O(1) regardless of total keys.
func (s *Store) FindAPIKeysByPrefix(ctx context.Context, prefix string) ([]apiKeyRow, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, key_prefix, key_hash, COALESCE(label,''), user_id, project_id, permissions, last_used_at, created_at
		FROM api_keys
		WHERE key_prefix = $1
	`, prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []apiKeyRow{}
	for rows.Next() {
		var r apiKeyRow
		if err := rows.Scan(&r.ID, &r.KeyPrefix, &r.KeyHash, &r.Label, &r.UserID, &r.ProjectID, &r.Permissions, &r.LastUsedAt, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type apiKeyRow struct {
	ID          uuid.UUID
	KeyPrefix   string
	KeyHash     string
	Label       string
	UserID      uuid.UUID
	ProjectID   uuid.UUID
	Permissions []byte
	LastUsedAt  *time.Time
	CreatedAt   time.Time
}

func (s *Store) TouchAPIKey(ctx context.Context, id uuid.UUID) error {
	_, err := s.Pool.Exec(ctx, `UPDATE api_keys SET last_used_at = NOW() WHERE id = $1`, id)
	return err
}

func (s *Store) ListAPIKeysForProject(ctx context.Context, projectID uuid.UUID) ([]models.APIKey, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, key_prefix, COALESCE(label,''), user_id, project_id, permissions, last_used_at, created_at
		FROM api_keys WHERE project_id = $1 ORDER BY created_at DESC
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []models.APIKey{}
	for rows.Next() {
		var k models.APIKey
		if err := rows.Scan(&k.ID, &k.KeyPrefix, &k.Label, &k.UserID, &k.ProjectID, &k.Permissions, &k.LastUsedAt, &k.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Store) DeleteAPIKey(ctx context.Context, id, projectID uuid.UUID) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM api_keys WHERE id = $1 AND project_id = $2`, id, projectID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// FindAPIKey is the helper auth middleware uses: parse prefix, scan
// candidates, bcrypt-compare. Returns ErrNotFound if no row matches.
func (s *Store) FindAPIKey(ctx context.Context, plaintext, prefix string, check func(hash, plain string) bool) (*apiKeyRow, error) {
	rows, err := s.FindAPIKeysByPrefix(ctx, prefix)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if check(r.KeyHash, plaintext) {
			return &r, nil
		}
	}
	return nil, ErrNotFound
}

// Forward-declare: pgx ErrNoRows for the future endpoints.
var _ = pgx.ErrNoRows
var _ = errors.New
