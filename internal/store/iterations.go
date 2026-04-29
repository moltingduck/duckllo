package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/moltingduck/duckllo/internal/models"
)

func (s *Store) AppendIteration(ctx context.Context, runID uuid.UUID, phase, role, provider, model, summary, transcriptURL string) (*models.Iteration, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Take an advisory transaction lock keyed off the run id so concurrent
	// AppendIteration calls for the same run serialise. We can't put
	// FOR UPDATE on an aggregate query, but pg_advisory_xact_lock
	// achieves the same goal cleanly. In practice the work_queue claim
	// ensures one runner at a time per run, but defending against
	// pathological clients is cheap.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('iter:' || $1::text))`, runID); err != nil {
		return nil, err
	}
	var nextIdx int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(idx), -1) + 1 FROM iterations WHERE run_id = $1
	`, runID).Scan(&nextIdx); err != nil {
		return nil, err
	}

	var it models.Iteration
	if err := tx.QueryRow(ctx, `
		INSERT INTO iterations (run_id, idx, phase, agent_role, provider, model, summary, transcript_url)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, run_id, idx, phase, agent_role, provider, model, summary, transcript_url,
		          prompt_tokens, completion_tokens, status, started_at, finished_at
	`, runID, nextIdx, phase, role, provider, model, summary, transcriptURL).Scan(
		&it.ID, &it.RunID, &it.Idx, &it.Phase, &it.AgentRole, &it.Provider, &it.Model,
		&it.Summary, &it.TranscriptURL, &it.PromptTokens, &it.CompletionTokens, &it.Status,
		&it.StartedAt, &it.FinishedAt,
	); err != nil {
		return nil, err
	}

	if _, err := tx.Exec(ctx, `UPDATE runs SET turns_used = turns_used + 1, updated_at = NOW() WHERE id = $1`, runID); err != nil {
		return nil, err
	}
	return &it, tx.Commit(ctx)
}

type IterationPatch struct {
	Summary          *string
	PromptTokens     *int
	CompletionTokens *int
	Status           *string
}

func (s *Store) UpdateIteration(ctx context.Context, id uuid.UUID, p IterationPatch) (*models.Iteration, error) {
	var it models.Iteration
	err := s.Pool.QueryRow(ctx, `
		UPDATE iterations SET
			summary = COALESCE($2, summary),
			prompt_tokens = COALESCE($3, prompt_tokens),
			completion_tokens = COALESCE($4, completion_tokens),
			status = COALESCE($5, status),
			finished_at = CASE WHEN $5 IN ('done','failed') THEN NOW() ELSE finished_at END
		WHERE id = $1
		RETURNING id, run_id, idx, phase, agent_role, provider, model, summary, transcript_url,
		          prompt_tokens, completion_tokens, status, started_at, finished_at
	`, id, p.Summary, p.PromptTokens, p.CompletionTokens, p.Status).Scan(
		&it.ID, &it.RunID, &it.Idx, &it.Phase, &it.AgentRole, &it.Provider, &it.Model,
		&it.Summary, &it.TranscriptURL, &it.PromptTokens, &it.CompletionTokens, &it.Status,
		&it.StartedAt, &it.FinishedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	// Roll prompt+completion deltas into the run's running token total.
	if p.PromptTokens != nil || p.CompletionTokens != nil {
		_, _ = s.Pool.Exec(ctx, `
			UPDATE runs SET token_usage = (
				SELECT COALESCE(SUM(prompt_tokens + completion_tokens), 0)
				FROM iterations WHERE run_id = $1
			), updated_at = NOW() WHERE id = $1
		`, it.RunID)
	}
	return &it, nil
}

func (s *Store) ListIterations(ctx context.Context, runID uuid.UUID) ([]models.Iteration, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, run_id, idx, phase, agent_role, provider, model, summary, transcript_url,
		       prompt_tokens, completion_tokens, status, started_at, finished_at
		FROM iterations WHERE run_id = $1 ORDER BY idx
	`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.Iteration{}
	for rows.Next() {
		var it models.Iteration
		if err := rows.Scan(&it.ID, &it.RunID, &it.Idx, &it.Phase, &it.AgentRole, &it.Provider, &it.Model,
			&it.Summary, &it.TranscriptURL, &it.PromptTokens, &it.CompletionTokens, &it.Status,
			&it.StartedAt, &it.FinishedAt); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}
