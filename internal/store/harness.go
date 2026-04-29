package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type Topology struct {
	ID             uuid.UUID       `json:"id"`
	ProjectID      uuid.UUID       `json:"project_id"`
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	DefaultGuides  json.RawMessage `json:"default_guides"`
	DefaultSensors json.RawMessage `json:"default_sensors"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

type HarnessRule struct {
	ID         uuid.UUID  `json:"id"`
	ProjectID  uuid.UUID  `json:"project_id"`
	TopologyID *uuid.UUID `json:"topology_id,omitempty"`
	Kind       string     `json:"kind"`
	Name       string     `json:"name"`
	Body       string     `json:"body"`
	Enabled    bool       `json:"enabled"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

func (s *Store) ListTopologies(ctx context.Context, projectID uuid.UUID) ([]Topology, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, project_id, name, description, default_guides, default_sensors, created_at, updated_at
		FROM topologies WHERE project_id = $1 ORDER BY name
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Topology{}
	for rows.Next() {
		var t Topology
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.Name, &t.Description, &t.DefaultGuides, &t.DefaultSensors, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) CreateTopology(ctx context.Context, projectID uuid.UUID, name, description string, guides, sensors []byte) (*Topology, error) {
	var t Topology
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO topologies (project_id, name, description, default_guides, default_sensors)
		VALUES ($1, $2, $3, COALESCE($4::jsonb, '[]'::jsonb), COALESCE($5::jsonb, '[]'::jsonb))
		RETURNING id, project_id, name, description, default_guides, default_sensors, created_at, updated_at
	`, projectID, name, description, nullableJSON(guides), nullableJSON(sensors)).Scan(
		&t.ID, &t.ProjectID, &t.Name, &t.Description, &t.DefaultGuides, &t.DefaultSensors, &t.CreatedAt, &t.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Store) ListEnabledRules(ctx context.Context, projectID uuid.UUID, topologyID *uuid.UUID) ([]HarnessRule, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, project_id, topology_id, kind, name, body, enabled, created_at, updated_at
		FROM harness_rules
		WHERE project_id = $1 AND enabled = TRUE
		  AND (topology_id IS NULL OR $2::uuid IS NULL OR topology_id = $2)
		ORDER BY kind, name
	`, projectID, topologyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HarnessRule{}
	for rows.Next() {
		var r HarnessRule
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.TopologyID, &r.Kind, &r.Name, &r.Body, &r.Enabled, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) CreateRule(ctx context.Context, projectID uuid.UUID, topologyID *uuid.UUID, kind, name, body string) (*HarnessRule, error) {
	var r HarnessRule
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO harness_rules (project_id, topology_id, kind, name, body)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, project_id, topology_id, kind, name, body, enabled, created_at, updated_at
	`, projectID, topologyID, kind, name, body).Scan(
		&r.ID, &r.ProjectID, &r.TopologyID, &r.Kind, &r.Name, &r.Body, &r.Enabled, &r.CreatedAt, &r.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Store) UpdateRule(ctx context.Context, id uuid.UUID, body *string, enabled *bool) (*HarnessRule, error) {
	var r HarnessRule
	err := s.Pool.QueryRow(ctx, `
		UPDATE harness_rules SET
			body    = COALESCE($2, body),
			enabled = COALESCE($3, enabled),
			updated_at = NOW()
		WHERE id = $1
		RETURNING id, project_id, topology_id, kind, name, body, enabled, created_at, updated_at
	`, id, body, enabled).Scan(&r.ID, &r.ProjectID, &r.TopologyID, &r.Kind, &r.Name, &r.Body, &r.Enabled, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Store) DeleteRule(ctx context.Context, id uuid.UUID) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM harness_rules WHERE id = $1`, id)
	return err
}
