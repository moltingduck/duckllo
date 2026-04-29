package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/moltingduck/duckllo/internal/models"
)

const runLockTTL = 90 * time.Second

// EnqueueRun creates a run for the given spec, optionally pre-bound to an
// approved plan. If planID is the zero UUID, the run starts in the 'plan'
// phase and the planner agent owns the first iteration; once it approves
// a new plan, SetRunPlan binds the run to it.
func (s *Store) EnqueueRun(ctx context.Context, specID uuid.UUID, planID *uuid.UUID, turnBudget int) (*models.Run, error) {
	if turnBudget <= 0 {
		turnBudget = 50
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var run models.Run
	if err := tx.QueryRow(ctx, `
		INSERT INTO runs (spec_id, plan_id, status, turn_budget)
		VALUES ($1, $2, 'queued', $3)
		RETURNING id, spec_id, COALESCE(plan_id, '00000000-0000-0000-0000-000000000000'::uuid),
		          status, COALESCE(runner_id,''), claimed_at, lock_expires_at,
		          workspace_meta, turn_budget, turns_used, token_usage,
		          started_at, finished_at, created_at, updated_at
	`, specID, planID, turnBudget).Scan(
		&run.ID, &run.SpecID, &run.PlanID, &run.Status, &run.RunnerID, &run.ClaimedAt, &run.LockExpiresAt,
		&run.WorkspaceMeta, &run.TurnBudget, &run.TurnsUsed, &run.TokenUsage,
		&run.StartedAt, &run.FinishedAt, &run.CreatedAt, &run.UpdatedAt,
	); err != nil {
		return nil, err
	}

	// First work item: 'plan' if no plan was bound, else 'execute'.
	startPhase := "execute"
	if planID == nil {
		startPhase = "plan"
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO work_queue (run_id, phase, status) VALUES ($1, $2, 'pending')
	`, run.ID, startPhase); err != nil {
		return nil, err
	}

	// Set the spec to 'running'.
	if _, err := tx.Exec(ctx, `
		UPDATE specs SET status = 'running', updated_at = NOW() WHERE id = $1
	`, specID); err != nil {
		return nil, err
	}

	return &run, tx.Commit(ctx)
}

func (s *Store) RunByID(ctx context.Context, id uuid.UUID) (*models.Run, error) {
	var run models.Run
	err := s.Pool.QueryRow(ctx, `
		SELECT id, spec_id, plan_id, status, COALESCE(runner_id,''), claimed_at, lock_expires_at,
		       workspace_meta, turn_budget, turns_used, token_usage,
		       started_at, finished_at, created_at, updated_at
		FROM runs WHERE id = $1
	`, id).Scan(&run.ID, &run.SpecID, &run.PlanID, &run.Status, &run.RunnerID, &run.ClaimedAt, &run.LockExpiresAt,
		&run.WorkspaceMeta, &run.TurnBudget, &run.TurnsUsed, &run.TokenUsage,
		&run.StartedAt, &run.FinishedAt, &run.CreatedAt, &run.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

// ClaimWork uses FOR UPDATE SKIP LOCKED to atomically grab the next pending
// (or stale-claimed) work item across all projects. Filters by allowed
// phases so a runner can opt into specific roles.
func (s *Store) ClaimWork(ctx context.Context, runnerID string, phases []string) (*models.WorkItem, *models.Run, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var item models.WorkItem
	expires := time.Now().Add(runLockTTL)
	err = tx.QueryRow(ctx, `
		WITH next AS (
			SELECT id FROM work_queue
			WHERE status = 'pending'
			   OR (status = 'claimed' AND lock_expires_at < NOW())
			AND ($2::text[] IS NULL OR phase = ANY($2::text[]))
			ORDER BY updated_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE work_queue w
		SET status = 'claimed',
		    claimed_by = $1,
		    claimed_at = NOW(),
		    lock_expires_at = $3,
		    attempts = attempts + 1,
		    updated_at = NOW()
		FROM next
		WHERE w.id = next.id
		RETURNING w.id, w.run_id, w.phase, w.status, COALESCE(w.claimed_by,''),
		          w.claimed_at, w.lock_expires_at, w.attempts, w.payload
	`, runnerID, phases, expires).Scan(&item.ID, &item.RunID, &item.Phase, &item.Status, &item.ClaimedBy,
		&item.ClaimedAt, &item.LockExpiresAt, &item.Attempts, &item.Payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}

	// Bind the runner to the run so future heartbeats / advances can verify.
	var run models.Run
	if err := tx.QueryRow(ctx, `
		UPDATE runs
		SET status = $2, runner_id = $1, claimed_at = NOW(), lock_expires_at = $3,
		    started_at = COALESCE(started_at, NOW()), updated_at = NOW()
		WHERE id = $4
		RETURNING id, spec_id, plan_id, status, COALESCE(runner_id,''), claimed_at, lock_expires_at,
		          workspace_meta, turn_budget, turns_used, token_usage,
		          started_at, finished_at, created_at, updated_at
	`, runnerID, runStatusForPhase(item.Phase), expires, item.RunID).Scan(
		&run.ID, &run.SpecID, &run.PlanID, &run.Status, &run.RunnerID, &run.ClaimedAt, &run.LockExpiresAt,
		&run.WorkspaceMeta, &run.TurnBudget, &run.TurnsUsed, &run.TokenUsage,
		&run.StartedAt, &run.FinishedAt, &run.CreatedAt, &run.UpdatedAt,
	); err != nil {
		return nil, nil, err
	}

	return &item, &run, tx.Commit(ctx)
}

// HeartbeatRun extends the lock on the run + active work item if and only
// if the runner is the current holder. Returns ErrNotFound otherwise so the
// runner knows it lost the lock and should stop work.
func (s *Store) HeartbeatRun(ctx context.Context, runID uuid.UUID, runnerID string) error {
	expires := time.Now().Add(runLockTTL)
	tag, err := s.Pool.Exec(ctx, `
		UPDATE runs SET lock_expires_at = $2, updated_at = NOW()
		WHERE id = $1 AND runner_id = $3 AND status NOT IN ('done','failed','aborted')
	`, runID, expires, runnerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	_, _ = s.Pool.Exec(ctx, `
		UPDATE work_queue SET lock_expires_at = $2, updated_at = NOW()
		WHERE run_id = $1 AND status = 'claimed' AND claimed_by = $3
	`, runID, expires, runnerID)
	return nil
}

// AdvanceRun marks the current work item done, optionally enqueues the next
// phase, and updates the run's status. Optionally binds a new plan_id —
// the planner uses this to attach the plan it just produced.
func (s *Store) AdvanceRun(ctx context.Context, runID uuid.UUID, runnerID, fromPhase, toPhase string, finalStatus string, planID *uuid.UUID) (*models.Run, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `
		UPDATE work_queue SET status = 'done', updated_at = NOW()
		WHERE run_id = $1 AND phase = $2 AND status = 'claimed' AND claimed_by = $3
	`, runID, fromPhase, runnerID); err != nil {
		return nil, err
	}

	if planID != nil {
		if _, err := tx.Exec(ctx, `UPDATE runs SET plan_id = $2 WHERE id = $1`, runID, *planID); err != nil {
			return nil, err
		}
	}

	if toPhase != "" {
		if _, err := tx.Exec(ctx, `
			INSERT INTO work_queue (run_id, phase, status) VALUES ($1, $2, 'pending')
		`, runID, toPhase); err != nil {
			return nil, err
		}
	}

	var run models.Run
	err = tx.QueryRow(ctx, `
		UPDATE runs SET
			status = COALESCE(NULLIF($2,''), status),
			finished_at = CASE WHEN $2 IN ('done','failed','aborted') THEN NOW() ELSE finished_at END,
			updated_at = NOW()
		WHERE id = $1 AND runner_id = $3
		RETURNING id, spec_id, plan_id, status, COALESCE(runner_id,''), claimed_at, lock_expires_at,
		          workspace_meta, turn_budget, turns_used, token_usage,
		          started_at, finished_at, created_at, updated_at
	`, runID, finalStatus, runnerID).Scan(&run.ID, &run.SpecID, &run.PlanID, &run.Status, &run.RunnerID,
		&run.ClaimedAt, &run.LockExpiresAt, &run.WorkspaceMeta, &run.TurnBudget, &run.TurnsUsed, &run.TokenUsage,
		&run.StartedAt, &run.FinishedAt, &run.CreatedAt, &run.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	if finalStatus == "done" {
		if _, err := tx.Exec(ctx, `UPDATE specs SET status = 'validated', updated_at = NOW() WHERE id = $1`, run.SpecID); err != nil {
			return nil, err
		}
	} else if finalStatus == "failed" || finalStatus == "aborted" {
		if _, err := tx.Exec(ctx, `UPDATE specs SET status = 'approved', updated_at = NOW() WHERE id = $1`, run.SpecID); err != nil {
			return nil, err
		}
	}
	return &run, tx.Commit(ctx)
}

// CompleteRunByHuman is the manual-resolve path: a human in the UI
// looks at a run parked in 'validating' or 'correcting', decides the
// criteria are good enough, and force-marks the run done. Unlike
// AdvanceRun this doesn't require a runner_id match (the human isn't
// the runner that originally claimed) and unconditionally sets the
// spec to 'validated'. Refuses to act on runs already in a terminal
// state.
func (s *Store) CompleteRunByHuman(ctx context.Context, runID uuid.UUID) (*models.Run, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Close any still-claimed work_queue items so a runner trying to
	// heartbeat them gets a clean 410 next time.
	_, _ = tx.Exec(ctx, `
		UPDATE work_queue SET status = 'done', updated_at = NOW()
		WHERE run_id = $1 AND status IN ('pending','claimed')
	`, runID)

	var run models.Run
	err = tx.QueryRow(ctx, `
		UPDATE runs SET
			status = 'done',
			finished_at = NOW(),
			updated_at = NOW()
		WHERE id = $1 AND status NOT IN ('done','failed','aborted')
		RETURNING id, spec_id, COALESCE(plan_id, '00000000-0000-0000-0000-000000000000'::uuid),
		          status, COALESCE(runner_id,''), claimed_at, lock_expires_at,
		          workspace_meta, turn_budget, turns_used, token_usage,
		          started_at, finished_at, created_at, updated_at
	`, runID).Scan(&run.ID, &run.SpecID, &run.PlanID, &run.Status, &run.RunnerID,
		&run.ClaimedAt, &run.LockExpiresAt, &run.WorkspaceMeta, &run.TurnBudget, &run.TurnsUsed, &run.TokenUsage,
		&run.StartedAt, &run.FinishedAt, &run.CreatedAt, &run.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE specs SET status = 'validated', updated_at = NOW() WHERE id = $1`, run.SpecID); err != nil {
		return nil, err
	}
	return &run, tx.Commit(ctx)
}

// SetRunPlan binds an in-flight run to the plan the planner just produced.
func (s *Store) SetRunPlan(ctx context.Context, runID, planID uuid.UUID) error {
	_, err := s.Pool.Exec(ctx, `UPDATE runs SET plan_id = $2, updated_at = NOW() WHERE id = $1`, runID, planID)
	return err
}

// SetWorkspaceMeta is what the runner posts after Docker provisioning so
// subsequent claims (and the bundle endpoint, used by sensors) know the
// container ID, dev URL, etc.
func (s *Store) SetWorkspaceMeta(ctx context.Context, runID uuid.UUID, meta []byte) error {
	if len(meta) == 0 {
		meta = []byte("{}")
	}
	_, err := s.Pool.Exec(ctx, `
		UPDATE runs SET workspace_meta = $2::jsonb, updated_at = NOW() WHERE id = $1
	`, runID, string(meta))
	return err
}

// AbortRun is the human-side kill switch. It does three things atomically
// so the system doesn't rot after an abort:
//  1. flips the run to status='aborted' (so a heartbeating runner will get
//     410 on its next ping and stop the orchestrator loop),
//  2. closes any pending/claimed work_queue rows for this run (otherwise a
//     fresh runner could still pick up the leftover phase), and
//  3. drops the spec back to 'approved' so the UI doesn't show it as
//     'running' forever and so a new run can be enqueued without manual
//     status surgery.
//
// Returns ErrNotFound if the run was already in a terminal state — the
// HTTP handler turns that into a clear 4xx instead of a misleading 204.
func (s *Store) AbortRun(ctx context.Context, runID uuid.UUID) (*models.Run, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var run models.Run
	err = tx.QueryRow(ctx, `
		UPDATE runs SET status = 'aborted', finished_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND status NOT IN ('done','failed','aborted')
		RETURNING id, spec_id, COALESCE(plan_id, '00000000-0000-0000-0000-000000000000'::uuid),
		          status, COALESCE(runner_id,''), claimed_at, lock_expires_at,
		          workspace_meta, turn_budget, turns_used, token_usage,
		          started_at, finished_at, created_at, updated_at
	`, runID).Scan(&run.ID, &run.SpecID, &run.PlanID, &run.Status, &run.RunnerID,
		&run.ClaimedAt, &run.LockExpiresAt, &run.WorkspaceMeta, &run.TurnBudget, &run.TurnsUsed, &run.TokenUsage,
		&run.StartedAt, &run.FinishedAt, &run.CreatedAt, &run.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE work_queue SET status = 'done', updated_at = NOW()
		WHERE run_id = $1 AND status IN ('pending','claimed')
	`, runID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE specs SET status = 'approved', updated_at = NOW()
		WHERE id = $1 AND status = 'running'
	`, run.SpecID); err != nil {
		return nil, err
	}
	return &run, tx.Commit(ctx)
}

// runStatusForPhase maps the work-queue phase the runner just claimed to
// the run's coarse-grained status field that the UI shows.
func runStatusForPhase(phase string) string {
	switch phase {
	case "plan":
		return "planning"
	case "execute":
		return "executing"
	case "validate":
		return "validating"
	case "correct":
		return "correcting"
	}
	return "executing"
}
