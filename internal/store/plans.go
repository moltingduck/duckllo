package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/moltingduck/duckllo/internal/models"
)

// CreatePlan adds a new plan version for a spec. The version is computed
// inside the same transaction as MAX(version)+1 so concurrent planner
// outputs don't collide.
func (s *Store) CreatePlan(ctx context.Context, specID uuid.UUID, createdBy *uuid.UUID, role string, steps []byte, dag []byte) (*models.Plan, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// See iterations.go for the rationale behind the advisory xact lock —
	// FOR UPDATE doesn't compose with MAX(); we serialise per-spec so
	// concurrent CreatePlan calls don't race on version numbering.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('plan:' || $1::text))`, specID); err != nil {
		return nil, err
	}
	var nextVersion int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(version), 0) + 1 FROM plans WHERE spec_id = $1
	`, specID).Scan(&nextVersion); err != nil {
		return nil, err
	}

	// Mark prior plans as superseded — only one approvable plan at a time.
	if _, err := tx.Exec(ctx, `
		UPDATE plans SET status = 'superseded', updated_at = NOW()
		WHERE spec_id = $1 AND status NOT IN ('approved','superseded')
	`, specID); err != nil {
		return nil, err
	}

	var p models.Plan
	if err := tx.QueryRow(ctx, `
		INSERT INTO plans (spec_id, version, created_by_role, steps, dag, created_by)
		VALUES ($1, $2, $3, COALESCE($4::jsonb, '[]'::jsonb), COALESCE($5::jsonb, '[]'::jsonb), $6)
		RETURNING id, spec_id, version, created_by_role, status, steps, dag,
		          created_by, approved_by, approved_at, created_at, updated_at
	`, specID, nextVersion, role, nullableJSON(steps), nullableJSON(dag), createdBy).Scan(
		&p.ID, &p.SpecID, &p.Version, &p.CreatedByRole, &p.Status, &p.Steps, &p.DAG,
		&p.CreatedBy, &p.ApprovedBy, &p.ApprovedAt, &p.CreatedAt, &p.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &p, tx.Commit(ctx)
}

func (s *Store) PlanByID(ctx context.Context, id uuid.UUID) (*models.Plan, error) {
	var p models.Plan
	err := s.Pool.QueryRow(ctx, `
		SELECT id, spec_id, version, created_by_role, status, steps, dag,
		       created_by, approved_by, approved_at, created_at, updated_at
		FROM plans WHERE id = $1
	`, id).Scan(&p.ID, &p.SpecID, &p.Version, &p.CreatedByRole, &p.Status, &p.Steps, &p.DAG,
		&p.CreatedBy, &p.ApprovedBy, &p.ApprovedAt, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) ListPlansForSpec(ctx context.Context, specID uuid.UUID) ([]models.Plan, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, spec_id, version, created_by_role, status, steps, dag,
		       created_by, approved_by, approved_at, created_at, updated_at
		FROM plans WHERE spec_id = $1 ORDER BY version DESC
	`, specID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.Plan{}
	for rows.Next() {
		var p models.Plan
		if err := rows.Scan(&p.ID, &p.SpecID, &p.Version, &p.CreatedByRole, &p.Status, &p.Steps, &p.DAG,
			&p.CreatedBy, &p.ApprovedBy, &p.ApprovedAt, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// LatestApprovedPlan is the runner's input: the most recent approved plan
// for a spec, or ErrNotFound if none exists.
func (s *Store) LatestApprovedPlan(ctx context.Context, specID uuid.UUID) (*models.Plan, error) {
	var p models.Plan
	err := s.Pool.QueryRow(ctx, `
		SELECT id, spec_id, version, created_by_role, status, steps, dag,
		       created_by, approved_by, approved_at, created_at, updated_at
		FROM plans WHERE spec_id = $1 AND status = 'approved'
		ORDER BY version DESC LIMIT 1
	`, specID).Scan(&p.ID, &p.SpecID, &p.Version, &p.CreatedByRole, &p.Status, &p.Steps, &p.DAG,
		&p.CreatedBy, &p.ApprovedBy, &p.ApprovedAt, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) UpdatePlanSteps(ctx context.Context, id uuid.UUID, steps, dag []byte) (*models.Plan, error) {
	var p models.Plan
	err := s.Pool.QueryRow(ctx, `
		UPDATE plans SET
			steps = COALESCE($2::jsonb, steps),
			dag   = COALESCE($3::jsonb, dag),
			updated_at = NOW()
		WHERE id = $1 AND status = 'draft'
		RETURNING id, spec_id, version, created_by_role, status, steps, dag,
		          created_by, approved_by, approved_at, created_at, updated_at
	`, id, nullableJSON(steps), nullableJSON(dag)).Scan(
		&p.ID, &p.SpecID, &p.Version, &p.CreatedByRole, &p.Status, &p.Steps, &p.DAG,
		&p.CreatedBy, &p.ApprovedBy, &p.ApprovedAt, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errors.New("plan not in draft state — make a new plan version instead")
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) ApprovePlan(ctx context.Context, id, approver uuid.UUID) (*models.Plan, error) {
	var p models.Plan
	err := s.Pool.QueryRow(ctx, `
		UPDATE plans
		SET status = 'approved', approved_by = $2, approved_at = $3, updated_at = NOW()
		WHERE id = $1 AND status = 'draft'
		RETURNING id, spec_id, version, created_by_role, status, steps, dag,
		          created_by, approved_by, approved_at, created_at, updated_at
	`, id, approver, time.Now()).Scan(&p.ID, &p.SpecID, &p.Version, &p.CreatedByRole, &p.Status, &p.Steps, &p.DAG,
		&p.CreatedBy, &p.ApprovedBy, &p.ApprovedAt, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errors.New("plan not in draft state")
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}
