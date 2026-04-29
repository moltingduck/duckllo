package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// AcceptanceCriterion lives inside specs.acceptance_criteria JSONB. Each
// criterion is *a typed sensor target*, not a free-form checkbox: the
// runner reads sensor_kind + sensor_spec to decide which sensor to fire,
// and posts a verification keyed back to id.
type AcceptanceCriterion struct {
	ID                  string         `json:"id"`
	Text                string         `json:"text"`
	SensorKind          string         `json:"sensor_kind"` // lint|test|screenshot|judge|manual ...
	SensorSpec          map[string]any `json:"sensor_spec,omitempty"`
	Satisfied           bool           `json:"satisfied"`
	LastVerificationID  *uuid.UUID     `json:"last_verification_id,omitempty"`
}

type ReferenceAsset struct {
	Kind  string `json:"kind"` // image | link | file
	URL   string `json:"url"`
	Label string `json:"label,omitempty"`
}

type Spec struct {
	ID                 uuid.UUID       `json:"id"`
	ProjectID          uuid.UUID       `json:"project_id"`
	TopologyID         *uuid.UUID      `json:"topology_id,omitempty"`
	Title              string          `json:"title"`
	Intent             string          `json:"intent"`
	Priority           string          `json:"priority"`
	Status             string          `json:"status"`
	AcceptanceCriteria json.RawMessage `json:"acceptance_criteria"`
	ReferenceAssets    json.RawMessage `json:"reference_assets"`
	AffectedComponents json.RawMessage `json:"affected_components"`
	CreatedBy          *uuid.UUID      `json:"created_by,omitempty"`
	AssigneeID         *uuid.UUID      `json:"assignee_id,omitempty"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

// PlanStep lives inside plans.steps JSONB.
type PlanStep struct {
	ID               string   `json:"id"`
	Order            int      `json:"order"`
	Summary          string   `json:"summary"`
	FilesTouched     []string `json:"files_touched,omitempty"`
	SensorsTargeted  []string `json:"sensors_targeted,omitempty"` // criterion IDs
	Notes            string   `json:"notes,omitempty"`
	Status           string   `json:"status,omitempty"` // pending | done | blocked
}

type Plan struct {
	ID            uuid.UUID       `json:"id"`
	SpecID        uuid.UUID       `json:"spec_id"`
	Version       int             `json:"version"`
	CreatedByRole string          `json:"created_by_role"`
	Status        string          `json:"status"`
	Steps         json.RawMessage `json:"steps"`
	DAG           json.RawMessage `json:"dag"`
	CreatedBy     *uuid.UUID      `json:"created_by,omitempty"`
	ApprovedBy    *uuid.UUID      `json:"approved_by,omitempty"`
	ApprovedAt    *time.Time      `json:"approved_at,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}
