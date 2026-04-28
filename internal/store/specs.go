package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/moltingduck/duckllo/internal/models"
)

func (s *Store) CreateSpec(ctx context.Context, projectID uuid.UUID, createdBy uuid.UUID, title, intent, priority string, topologyID *uuid.UUID) (*models.Spec, error) {
	var sp models.Spec
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO specs (project_id, topology_id, title, intent, priority, created_by)
		VALUES ($1, $2, $3, $4, COALESCE(NULLIF($5,''), 'medium'), $6)
		RETURNING id, project_id, topology_id, title, intent, priority, status,
		          acceptance_criteria, reference_assets, affected_components,
		          created_by, assignee_id, created_at, updated_at
	`, projectID, topologyID, title, intent, priority, createdBy).Scan(
		&sp.ID, &sp.ProjectID, &sp.TopologyID, &sp.Title, &sp.Intent, &sp.Priority, &sp.Status,
		&sp.AcceptanceCriteria, &sp.ReferenceAssets, &sp.AffectedComponents,
		&sp.CreatedBy, &sp.AssigneeID, &sp.CreatedAt, &sp.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &sp, nil
}

func (s *Store) ListSpecs(ctx context.Context, projectID uuid.UUID, status string) ([]models.Spec, error) {
	q := `
		SELECT id, project_id, topology_id, title, intent, priority, status,
		       acceptance_criteria, reference_assets, affected_components,
		       created_by, assignee_id, created_at, updated_at
		FROM specs WHERE project_id = $1
	`
	args := []any{projectID}
	if status != "" {
		q += ` AND status = $2`
		args = append(args, status)
	}
	q += ` ORDER BY updated_at DESC`

	rows, err := s.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.Spec{}
	for rows.Next() {
		var sp models.Spec
		if err := rows.Scan(&sp.ID, &sp.ProjectID, &sp.TopologyID, &sp.Title, &sp.Intent, &sp.Priority, &sp.Status,
			&sp.AcceptanceCriteria, &sp.ReferenceAssets, &sp.AffectedComponents,
			&sp.CreatedBy, &sp.AssigneeID, &sp.CreatedAt, &sp.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, sp)
	}
	return out, rows.Err()
}

func (s *Store) SpecByID(ctx context.Context, id uuid.UUID) (*models.Spec, error) {
	var sp models.Spec
	err := s.Pool.QueryRow(ctx, `
		SELECT id, project_id, topology_id, title, intent, priority, status,
		       acceptance_criteria, reference_assets, affected_components,
		       created_by, assignee_id, created_at, updated_at
		FROM specs WHERE id = $1
	`, id).Scan(&sp.ID, &sp.ProjectID, &sp.TopologyID, &sp.Title, &sp.Intent, &sp.Priority, &sp.Status,
		&sp.AcceptanceCriteria, &sp.ReferenceAssets, &sp.AffectedComponents,
		&sp.CreatedBy, &sp.AssigneeID, &sp.CreatedAt, &sp.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &sp, nil
}

type SpecPatch struct {
	Title              *string
	Intent             *string
	Priority           *string
	Status             *string
	AssigneeID         *uuid.UUID
	AcceptanceCriteria []byte // raw JSONB; nil means leave alone
	ReferenceAssets    []byte
	AffectedComponents []byte
}

func (s *Store) UpdateSpec(ctx context.Context, id uuid.UUID, p SpecPatch) (*models.Spec, error) {
	var sp models.Spec
	err := s.Pool.QueryRow(ctx, `
		UPDATE specs SET
			title              = COALESCE($2, title),
			intent             = COALESCE($3, intent),
			priority           = COALESCE($4, priority),
			status             = COALESCE($5, status),
			assignee_id        = COALESCE($6, assignee_id),
			acceptance_criteria = COALESCE($7::jsonb, acceptance_criteria),
			reference_assets    = COALESCE($8::jsonb, reference_assets),
			affected_components = COALESCE($9::jsonb, affected_components),
			updated_at         = NOW()
		WHERE id = $1
		RETURNING id, project_id, topology_id, title, intent, priority, status,
		          acceptance_criteria, reference_assets, affected_components,
		          created_by, assignee_id, created_at, updated_at
	`,
		id,
		p.Title, p.Intent, p.Priority, p.Status, p.AssigneeID,
		nullableJSON(p.AcceptanceCriteria), nullableJSON(p.ReferenceAssets), nullableJSON(p.AffectedComponents),
	).Scan(&sp.ID, &sp.ProjectID, &sp.TopologyID, &sp.Title, &sp.Intent, &sp.Priority, &sp.Status,
		&sp.AcceptanceCriteria, &sp.ReferenceAssets, &sp.AffectedComponents,
		&sp.CreatedBy, &sp.AssigneeID, &sp.CreatedAt, &sp.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &sp, nil
}

// nullableJSON converts a nil/empty []byte to a SQL NULL so COALESCE keeps
// the existing column value. A non-nil []byte is passed through.
func nullableJSON(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}

// AppendCriterion adds a new acceptance criterion. Returns the updated spec.
func (s *Store) AppendCriterion(ctx context.Context, specID uuid.UUID, c models.AcceptanceCriterion) (*models.Spec, error) {
	if c.ID == "" {
		c.ID = uuid.NewString()
	}
	body, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	var sp models.Spec
	err = s.Pool.QueryRow(ctx, `
		UPDATE specs
		SET acceptance_criteria = COALESCE(acceptance_criteria, '[]'::jsonb) || jsonb_build_array($2::jsonb),
		    updated_at = NOW()
		WHERE id = $1
		RETURNING id, project_id, topology_id, title, intent, priority, status,
		          acceptance_criteria, reference_assets, affected_components,
		          created_by, assignee_id, created_at, updated_at
	`, specID, string(body)).Scan(&sp.ID, &sp.ProjectID, &sp.TopologyID, &sp.Title, &sp.Intent, &sp.Priority, &sp.Status,
		&sp.AcceptanceCriteria, &sp.ReferenceAssets, &sp.AffectedComponents,
		&sp.CreatedBy, &sp.AssigneeID, &sp.CreatedAt, &sp.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &sp, nil
}
