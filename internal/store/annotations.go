package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/moltingduck/duckllo/internal/models"
)

type AnnotationInput struct {
	VerificationID uuid.UUID
	AuthorID       *uuid.UUID
	BBox           []byte
	Body           string
	Verdict        string
}

// AnnotationResult bundles the annotation row with the side effects a
// fix_required dispute may have triggered. When DisputedDoneRun is true,
// the parent run was flipped from 'done' back to 'correcting' (and the
// parent spec from 'validated' back to 'running') so the HTTP handler
// can publish run.advanced + spec.updated over SSE. Without that signal
// the dashboard would still show the run as done after a dispute.
type AnnotationResult struct {
	Annotation       *models.Annotation
	DisputedDoneRun  bool
	Run              *models.Run
	Spec             *models.Spec
}

// CreateAnnotation inserts the annotation row and, for fix_required
// verdicts, applies the dispute side effects in a single transaction:
//
//  1. Look up the parent run + spec.
//  2. If the spec is 'merged' (terminal, shipped state), return
//     ErrSpecMerged — disputes against a merged contract are refused
//     to avoid silently re-opening a shipped change.
//  3. Flip the run to 'correcting' (covers both still-in-flight runs
//     and 'done' runs whose verification a human now disagrees with).
//     'failed' and 'aborted' stay terminal — those represent a runner
//     that gave up, not a validator that returned the wrong verdict.
//  4. Flip the spec to 'running' if it was 'validated', so the UI
//     stops showing a green ribbon over a re-opened contract.
//  5. Idempotently enqueue a 'correct' work_queue row so a corrector
//     agent has something to claim. Status without a queue entry
//     leaves the run permanently stuck.
func (s *Store) CreateAnnotation(ctx context.Context, in AnnotationInput) (*AnnotationResult, error) {
	if in.Verdict == "" {
		in.Verdict = "fix_required"
	}

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// For fix_required, peek at the parent spec status before inserting
	// so a merged-spec dispute is rejected without leaving an orphan
	// annotation behind that would mislead a reader of the verification
	// timeline.
	var specStatus string
	var runStatus string
	if in.Verdict == "fix_required" {
		err := tx.QueryRow(ctx, `
			SELECT s.status, r.status
			  FROM verifications v
			  JOIN runs  r ON r.id = v.run_id
			  JOIN specs s ON s.id = r.spec_id
			 WHERE v.id = $1
		`, in.VerificationID).Scan(&specStatus, &runStatus)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		if specStatus == "merged" {
			return nil, ErrSpecMerged
		}
	}

	var a models.Annotation
	err = tx.QueryRow(ctx, `
		INSERT INTO annotations (verification_id, author_id, bbox, body, verdict)
		VALUES ($1, $2, COALESCE($3::jsonb, '{}'::jsonb), $4, $5)
		RETURNING id, verification_id, author_id, bbox, body, verdict, resolved, created_at
	`, in.VerificationID, in.AuthorID, nullableJSON(in.BBox), in.Body, in.Verdict).Scan(
		&a.ID, &a.VerificationID, &a.AuthorID, &a.BBox, &a.Body, &a.Verdict, &a.Resolved, &a.CreatedAt,
	)
	if err != nil {
		return nil, err
	}

	result := &AnnotationResult{Annotation: &a}

	if in.Verdict == "fix_required" {
		// Flip the run to 'correcting'. 'failed' and 'aborted' are
		// unrecoverable terminal states (the runner gave up / a human
		// aborted) — don't resurrect those; only 'done' (validator
		// said pass) is the case we want to overturn here.
		var run models.Run
		err = tx.QueryRow(ctx, `
			UPDATE runs SET status = 'correcting', finished_at = NULL, updated_at = NOW()
			WHERE id = (SELECT v.run_id FROM verifications v WHERE v.id = $1)
			  AND status NOT IN ('failed','aborted')
			RETURNING id, spec_id, COALESCE(plan_id, '00000000-0000-0000-0000-000000000000'::uuid),
			          status, COALESCE(runner_id,''), claimed_at, lock_expires_at,
			          workspace_meta, turn_budget, turns_used, token_usage,
			          started_at, finished_at, created_at, updated_at
		`, in.VerificationID).Scan(
			&run.ID, &run.SpecID, &run.PlanID, &run.Status, &run.RunnerID, &run.ClaimedAt, &run.LockExpiresAt,
			&run.WorkspaceMeta, &run.TurnBudget, &run.TurnsUsed, &run.TokenUsage,
			&run.StartedAt, &run.FinishedAt, &run.CreatedAt, &run.UpdatedAt,
		)
		if err == nil {
			result.Run = &run
			// Spec follows the run: a re-opened run drags a validated
			// spec back to 'running' so the UI doesn't lie about
			// completion. Skip if already running/approved/etc.
			var sp models.Spec
			err = tx.QueryRow(ctx, `
				UPDATE specs SET status = 'running', updated_at = NOW()
				WHERE id = $1 AND status = 'validated'
				RETURNING id, project_id, topology_id, title, intent, priority, status,
				          acceptance_criteria, reference_assets, affected_components,
				          created_by, assignee_id, created_at, updated_at
			`, run.SpecID).Scan(
				&sp.ID, &sp.ProjectID, &sp.TopologyID, &sp.Title, &sp.Intent, &sp.Priority, &sp.Status,
				&sp.AcceptanceCriteria, &sp.ReferenceAssets, &sp.AffectedComponents,
				&sp.CreatedBy, &sp.AssigneeID, &sp.CreatedAt, &sp.UpdatedAt,
			)
			if err == nil {
				result.Spec = &sp
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return nil, err
			}
			// A dispute against a 'done' run is exactly the case the
			// handler needs to broadcast — the dashboard otherwise
			// keeps showing it as terminal.
			if runStatus == "done" {
				result.DisputedDoneRun = true
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}

		// Idempotent work-queue insert: a 'correcting' run with no
		// pending/claimed 'correct' item sits forever. NOT EXISTS keeps
		// repeated fix_required clicks during one correction cycle from
		// piling duplicates.
		if _, err := tx.Exec(ctx, `
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
		`, in.VerificationID); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
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
