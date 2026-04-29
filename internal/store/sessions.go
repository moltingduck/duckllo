package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/moltingduck/duckllo/internal/models"
)

const sessionTTL = 30 * 24 * time.Hour

func (s *Store) CreateSession(ctx context.Context, userID uuid.UUID) (*models.Session, error) {
	expires := time.Now().Add(sessionTTL)
	var sess models.Session
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO sessions (user_id, expires_at)
		VALUES ($1, $2)
		RETURNING token, user_id, created_at, expires_at
	`, userID, expires).Scan(&sess.Token, &sess.UserID, &sess.CreatedAt, &sess.ExpiresAt)
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

func (s *Store) SessionByToken(ctx context.Context, token uuid.UUID) (*models.Session, error) {
	var sess models.Session
	err := s.Pool.QueryRow(ctx, `
		SELECT token, user_id, created_at, expires_at
		FROM sessions
		WHERE token = $1 AND expires_at > NOW()
	`, token).Scan(&sess.Token, &sess.UserID, &sess.CreatedAt, &sess.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

func (s *Store) DeleteSession(ctx context.Context, token uuid.UUID) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM sessions WHERE token = $1`, token)
	return err
}

// TouchSession implements sliding expiration: if the session has less than
// renewBefore time remaining, push expires_at out to NOW() + sessionTTL.
// Returning early when the lease is fresh keeps this to roughly one UPDATE
// per session per day instead of one per request.
func (s *Store) TouchSession(ctx context.Context, token uuid.UUID) error {
	const renewBefore = sessionTTL - 24*time.Hour
	_, err := s.Pool.Exec(ctx, `
		UPDATE sessions SET expires_at = NOW() + $2::interval
		WHERE token = $1 AND expires_at < NOW() + $3::interval
	`, token, sessionTTL.String(), renewBefore.String())
	return err
}
