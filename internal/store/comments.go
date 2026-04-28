package store

import (
	"context"

	"github.com/google/uuid"

	"github.com/moltingduck/duckllo/internal/models"
)

func (s *Store) CreateComment(ctx context.Context, projectID uuid.UUID, authorID *uuid.UUID, targetKind string, targetID uuid.UUID, body string) (*models.Comment, error) {
	var c models.Comment
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO comments (project_id, target_kind, target_id, author_id, body)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, project_id, target_kind, target_id, author_id, body, created_at
	`, projectID, targetKind, targetID, authorID, body).Scan(
		&c.ID, &c.ProjectID, &c.TargetKind, &c.TargetID, &c.AuthorID, &c.Body, &c.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) ListComments(ctx context.Context, targetKind string, targetID uuid.UUID) ([]models.Comment, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, project_id, target_kind, target_id, author_id, body, created_at
		FROM comments WHERE target_kind = $1 AND target_id = $2 ORDER BY created_at
	`, targetKind, targetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.Comment{}
	for rows.Next() {
		var c models.Comment
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.TargetKind, &c.TargetID, &c.AuthorID, &c.Body, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
