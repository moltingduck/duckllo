package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type Run struct {
	ID            uuid.UUID       `json:"id"`
	SpecID        uuid.UUID       `json:"spec_id"`
	PlanID        uuid.UUID       `json:"plan_id"`
	Status        string          `json:"status"`
	RunnerID      string          `json:"runner_id,omitempty"`
	ClaimedAt     *time.Time      `json:"claimed_at,omitempty"`
	LockExpiresAt *time.Time      `json:"lock_expires_at,omitempty"`
	WorkspaceMeta json.RawMessage `json:"workspace_meta"`
	TurnBudget    int             `json:"turn_budget"`
	TurnsUsed     int             `json:"turns_used"`
	TokenUsage    int             `json:"token_usage"`
	StartedAt     *time.Time      `json:"started_at,omitempty"`
	FinishedAt    *time.Time      `json:"finished_at,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

type Iteration struct {
	ID               uuid.UUID  `json:"id"`
	RunID            uuid.UUID  `json:"run_id"`
	Idx              int        `json:"idx"`
	Phase            string     `json:"phase"`
	AgentRole        string     `json:"agent_role"`
	Provider         string     `json:"provider"`
	Model            string     `json:"model"`
	Summary          string     `json:"summary"`
	Transcript       string     `json:"transcript"`
	TranscriptURL    string     `json:"transcript_url"`
	PromptTokens     int        `json:"prompt_tokens"`
	CompletionTokens int        `json:"completion_tokens"`
	Status           string     `json:"status"`
	StartedAt        time.Time  `json:"started_at"`
	FinishedAt       *time.Time `json:"finished_at,omitempty"`
}

type WorkItem struct {
	ID            uuid.UUID       `json:"id"`
	RunID         uuid.UUID       `json:"run_id"`
	Phase         string          `json:"phase"`
	Status        string          `json:"status"`
	ClaimedBy     string          `json:"claimed_by,omitempty"`
	ClaimedAt     *time.Time      `json:"claimed_at,omitempty"`
	LockExpiresAt *time.Time      `json:"lock_expires_at,omitempty"`
	Attempts      int             `json:"attempts"`
	Payload       json.RawMessage `json:"payload"`
}
