package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type Verification struct {
	ID          uuid.UUID       `json:"id"`
	RunID       uuid.UUID       `json:"run_id"`
	IterationID *uuid.UUID      `json:"iteration_id,omitempty"`
	CriterionID string          `json:"criterion_id,omitempty"`
	Kind        string          `json:"kind"`
	Class       string          `json:"class"`
	Direction   string          `json:"direction"`
	Status      string          `json:"status"`
	Summary     string          `json:"summary"`
	ArtifactURL string          `json:"artifact_url"`
	DetailsJSON json.RawMessage `json:"details_json"`
	CreatedAt   time.Time       `json:"created_at"`
}

type Annotation struct {
	ID             uuid.UUID       `json:"id"`
	VerificationID uuid.UUID       `json:"verification_id"`
	AuthorID       *uuid.UUID      `json:"author_id,omitempty"`
	BBox           json.RawMessage `json:"bbox"`
	Body           string          `json:"body"`
	Verdict        string          `json:"verdict"`
	Resolved       bool            `json:"resolved"`
	CreatedAt      time.Time       `json:"created_at"`
}

type Comment struct {
	ID         uuid.UUID  `json:"id"`
	ProjectID  uuid.UUID  `json:"project_id"`
	TargetKind string     `json:"target_kind"`
	TargetID   uuid.UUID  `json:"target_id"`
	AuthorID   *uuid.UUID `json:"author_id,omitempty"`
	Body       string     `json:"body"`
	CreatedAt  time.Time  `json:"created_at"`
}
