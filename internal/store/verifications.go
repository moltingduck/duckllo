package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/moltingduck/duckllo/internal/models"
)

type VerificationInput struct {
	RunID       uuid.UUID
	IterationID *uuid.UUID
	CriterionID string
	Kind        string
	Class       string
	Direction   string
	Status      string
	Summary     string
	ArtifactURL string
	Details     []byte
}

func (s *Store) CreateVerification(ctx context.Context, v VerificationInput) (*models.Verification, error) {
	if v.Direction == "" {
		v.Direction = "feedback"
	}
	if v.Status == "" {
		v.Status = "pending"
	}
	var out models.Verification
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO verifications (run_id, iteration_id, criterion_id, kind, class, direction, status, summary, artifact_url, details_json)
		VALUES ($1, $2, NULLIF($3,''), $4, $5, $6, $7, $8, $9, COALESCE($10::jsonb, '{}'::jsonb))
		RETURNING id, run_id, iteration_id, COALESCE(criterion_id,''), kind, class, direction, status,
		          summary, artifact_url, details_json, created_at
	`, v.RunID, v.IterationID, v.CriterionID, v.Kind, v.Class, v.Direction, v.Status, v.Summary, v.ArtifactURL, nullableJSON(v.Details)).Scan(
		&out.ID, &out.RunID, &out.IterationID, &out.CriterionID, &out.Kind, &out.Class, &out.Direction, &out.Status,
		&out.Summary, &out.ArtifactURL, &out.DetailsJSON, &out.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	if v.CriterionID != "" {
		// Mirror pass/fail back into the spec's acceptance_criteria so the UI
		// can render green/red without scanning verifications.
		_, _ = s.Pool.Exec(ctx, `
			UPDATE specs SET acceptance_criteria = (
				SELECT jsonb_agg(
					CASE WHEN c->>'id' = $2
						THEN jsonb_set(jsonb_set(c, '{satisfied}', to_jsonb($3::bool)),
						                '{last_verification_id}', to_jsonb($4::text))
						ELSE c
					END)
				FROM jsonb_array_elements(acceptance_criteria) AS c
			), updated_at = NOW()
			WHERE id = (SELECT spec_id FROM runs WHERE id = $1)
		`, v.RunID, v.CriterionID, v.Status == "pass", out.ID.String())
	}
	return &out, nil
}

type VerificationPatch struct {
	Status  *string
	Summary *string
}

func (s *Store) UpdateVerification(ctx context.Context, id uuid.UUID, p VerificationPatch) (*models.Verification, error) {
	var out models.Verification
	err := s.Pool.QueryRow(ctx, `
		UPDATE verifications SET
			status  = COALESCE($2, status),
			summary = COALESCE($3, summary)
		WHERE id = $1
		RETURNING id, run_id, iteration_id, COALESCE(criterion_id,''), kind, class, direction, status,
		          summary, artifact_url, details_json, created_at
	`, id, p.Status, p.Summary).Scan(&out.ID, &out.RunID, &out.IterationID, &out.CriterionID, &out.Kind,
		&out.Class, &out.Direction, &out.Status, &out.Summary, &out.ArtifactURL, &out.DetailsJSON, &out.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *Store) ListVerificationsForRun(ctx context.Context, runID uuid.UUID) ([]models.Verification, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, run_id, iteration_id, COALESCE(criterion_id,''), kind, class, direction, status,
		       summary, artifact_url, details_json, created_at
		FROM verifications WHERE run_id = $1 ORDER BY created_at
	`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.Verification{}
	for rows.Next() {
		var v models.Verification
		if err := rows.Scan(&v.ID, &v.RunID, &v.IterationID, &v.CriterionID, &v.Kind, &v.Class, &v.Direction, &v.Status,
			&v.Summary, &v.ArtifactURL, &v.DetailsJSON, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) VerificationByID(ctx context.Context, id uuid.UUID) (*models.Verification, error) {
	var v models.Verification
	err := s.Pool.QueryRow(ctx, `
		SELECT id, run_id, iteration_id, COALESCE(criterion_id,''), kind, class, direction, status,
		       summary, artifact_url, details_json, created_at
		FROM verifications WHERE id = $1
	`, id).Scan(&v.ID, &v.RunID, &v.IterationID, &v.CriterionID, &v.Kind, &v.Class, &v.Direction, &v.Status,
		&v.Summary, &v.ArtifactURL, &v.DetailsJSON, &v.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}
