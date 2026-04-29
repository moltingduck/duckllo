package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// RecurringFailure aggregates verifications that have failed (or warned)
// repeatedly for the same (spec, criterion) pair over the last 30 days.
// Surfaced in the steering loop so humans can encode a rule for the
// pattern instead of letting the agent re-fail on it.
type RecurringFailure struct {
	SpecID        uuid.UUID `json:"spec_id"`
	SpecTitle     string    `json:"spec_title"`
	CriterionID   string    `json:"criterion_id"`
	CriterionText string    `json:"criterion_text"`
	Kind          string    `json:"kind"`
	FailCount     int       `json:"fail_count"`
	LastSeen      time.Time `json:"last_seen"`
	LastSummary   string    `json:"last_summary"`
}

func (s *Store) RecurringFailures(ctx context.Context, projectID uuid.UUID) ([]RecurringFailure, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT
			r.spec_id,
			sp.title,
			v.criterion_id,
			v.kind,
			COUNT(*)::int AS fail_count,
			MAX(v.created_at) AS last_seen,
			COALESCE(
				(SELECT c->>'text'
				   FROM jsonb_array_elements(sp.acceptance_criteria) c
				  WHERE c->>'id' = v.criterion_id
				  LIMIT 1),
				''
			) AS criterion_text,
			COALESCE(
				(SELECT v2.summary
				   FROM verifications v2
				   JOIN runs r2 ON r2.id = v2.run_id
				  WHERE r2.spec_id = r.spec_id
				    AND v2.criterion_id = v.criterion_id
				    AND v2.status IN ('fail','warn')
				  ORDER BY v2.created_at DESC
				  LIMIT 1),
				''
			) AS last_summary
		FROM verifications v
		JOIN runs r ON r.id = v.run_id
		JOIN specs sp ON sp.id = r.spec_id
		WHERE sp.project_id = $1
		  AND v.status IN ('fail','warn')
		  AND COALESCE(v.criterion_id,'') <> ''
		  AND v.created_at >= NOW() - INTERVAL '30 days'
		GROUP BY r.spec_id, sp.title, v.criterion_id, v.kind, sp.acceptance_criteria
		HAVING COUNT(*) >= 2
		ORDER BY fail_count DESC, last_seen DESC
		LIMIT 50
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []RecurringFailure{}
	for rows.Next() {
		var f RecurringFailure
		if err := rows.Scan(&f.SpecID, &f.SpecTitle, &f.CriterionID, &f.Kind,
			&f.FailCount, &f.LastSeen, &f.CriterionText, &f.LastSummary); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
