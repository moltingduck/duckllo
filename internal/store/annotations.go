package store

import (
	"context"

	"github.com/google/uuid"

	"github.com/moltingduck/duckllo/internal/models"
)

type AnnotationInput struct {
	VerificationID uuid.UUID
	AuthorID       *uuid.UUID
	BBox           []byte
	Body           string
	Verdict        string
}

func (s *Store) CreateAnnotation(ctx context.Context, in AnnotationInput) (*models.Annotation, error) {
	if in.Verdict == "" {
		in.Verdict = "fix_required"
	}
	var a models.Annotation
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO annotations (verification_id, author_id, bbox, body, verdict)
		VALUES ($1, $2, COALESCE($3::jsonb, '{}'::jsonb), $4, $5)
		RETURNING id, verification_id, author_id, bbox, body, verdict, resolved, created_at
	`, in.VerificationID, in.AuthorID, nullableJSON(in.BBox), in.Body, in.Verdict).Scan(
		&a.ID, &a.VerificationID, &a.AuthorID, &a.BBox, &a.Body, &a.Verdict, &a.Resolved, &a.CreatedAt,
	)
	if err != nil {
		return nil, err
	}

	// Posting a fix_required annotation flips the parent run into
	// 'correcting' AND enqueues a 'correct' work item. Without the
	// queue insertion the runner has nothing to claim — the run sits
	// in 'correcting' status forever and the corrector never fires.
	// Idempotent: if a correct item is already pending for the run,
	// the WHERE clause prevents a duplicate.
	if in.Verdict == "fix_required" {
		tx, err := s.Pool.Begin(ctx)
		if err == nil {
			defer tx.Rollback(ctx) //nolint:errcheck
			_, _ = tx.Exec(ctx, `
				UPDATE runs SET status = 'correcting', updated_at = NOW()
				WHERE id = (SELECT v.run_id FROM verifications v WHERE v.id = $1)
				  AND status NOT IN ('done','failed','aborted')
			`, in.VerificationID)
			_, _ = tx.Exec(ctx, `
				INSERT INTO work_queue (run_id, phase, status)
				SELECT v.run_id, 'correct', 'pending'
				  FROM verifications v
				 WHERE v.id = $1
				   AND NOT EXISTS (
					   SELECT 1 FROM work_queue w
					    WHERE w.run_id = v.run_id
					      AND w.phase  = 'correct'
					      AND w.status IN ('pending','claimed')
				   )
			`, in.VerificationID)
			_ = tx.Commit(ctx)
		}
	}
	return &a, nil
}

func (s *Store) ListAnnotations(ctx context.Context, verificationID uuid.UUID) ([]models.Annotation, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, verification_id, author_id, bbox, body, verdict, resolved, created_at
		FROM annotations WHERE verification_id = $1 ORDER BY created_at
	`, verificationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.Annotation{}
	for rows.Next() {
		var a models.Annotation
		if err := rows.Scan(&a.ID, &a.VerificationID, &a.AuthorID, &a.BBox, &a.Body, &a.Verdict, &a.Resolved, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListOpenAnnotationsForRun is what the bundle endpoint returns to runners
// during the correct phase: every fix_required annotation that has not been
// marked resolved.
func (s *Store) ListOpenAnnotationsForRun(ctx context.Context, runID uuid.UUID) ([]models.Annotation, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT a.id, a.verification_id, a.author_id, a.bbox, a.body, a.verdict, a.resolved, a.created_at
		FROM annotations a
		JOIN verifications v ON v.id = a.verification_id
		WHERE v.run_id = $1 AND a.verdict = 'fix_required' AND a.resolved = FALSE
		ORDER BY a.created_at
	`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.Annotation{}
	for rows.Next() {
		var a models.Annotation
		if err := rows.Scan(&a.ID, &a.VerificationID, &a.AuthorID, &a.BBox, &a.Body, &a.Verdict, &a.Resolved, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) ResolveAnnotation(ctx context.Context, id uuid.UUID) error {
	_, err := s.Pool.Exec(ctx, `UPDATE annotations SET resolved = TRUE WHERE id = $1`, id)
	return err
}
