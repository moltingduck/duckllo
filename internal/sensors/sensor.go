// Package sensors implements the runner-side feedback signals: lint,
// typecheck, tests, build, screenshot, and gif. The inferential 'judge'
// sensor is implemented by the orchestrator itself (LLM-as-judge) and
// does not appear here.
//
// The Martin Fowler taxonomy distinguishes computational (deterministic,
// fast, cheap) sensors from inferential ones; both are first-class but
// computational sensors should run on every iteration since they are
// trustworthy.
package sensors

import (
	"context"
	"encoding/json"
	"errors"
)

// _ ensures the context import is consistently referenced even on platforms
// where the rest of the file doesn't end up using it directly.
var _ = context.Canceled

// Result is what every sensor produces. It maps almost 1:1 onto the
// duckllo verifications POST payload.
type Result struct {
	Status      string         // pass | fail | warn | skipped
	Class       string         // computational | inferential | human
	Summary     string
	Details     map[string]any
	ArtifactBytes []byte       // optional binary blob (PNG, GIF, log)
	ContentType   string       // matches ArtifactBytes
	FileName      string       // hint for the upload
}

// Sensor is the runner-side counterpart of an acceptance criterion.
// Implementations live next to this file (shell.go, screenshot.go, ...).
type Sensor interface {
	Kind() string
	Run(ctx context.Context, c Criterion, env Env) (*Result, error)
}

// Criterion is the typed view of a spec acceptance_criteria entry. JSON
// shape mirrors what the API stores.
type Criterion struct {
	ID         string         `json:"id"`
	Text       string         `json:"text"`
	SensorKind string         `json:"sensor_kind"`
	SensorSpec map[string]any `json:"sensor_spec,omitempty"`
}

// Env is the bag of context a sensor needs at runtime: the workspace it
// reads/writes, the dev-server URL it can probe, a logger sink, and a
// callback to fetch artifacts (baseline screenshots, reference assets)
// from the duckllo server. Fetch may be nil — sensors should treat that
// as "no historical artifacts available" and degrade gracefully.
type Env struct {
	WorkspaceDir string
	DevURL       string
	ChromePath   string
	LogF         func(format string, args ...any)
	Fetch        func(ctx context.Context, url string) ([]byte, error)
}

// ErrNotApplicable means the criterion's sensor_kind is not supported by
// this sensor; the registry uses it to skip cleanly.
var ErrNotApplicable = errors.New("sensor not applicable")

// MarshalDetails is a convenience for sensors that want to attach
// structured payload to a verification.
func MarshalDetails(d any) map[string]any {
	if d == nil {
		return nil
	}
	b, err := json.Marshal(d)
	if err != nil {
		return nil
	}
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}
