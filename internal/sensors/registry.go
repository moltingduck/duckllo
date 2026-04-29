package sensors

import "sync"

// Registry maps a criterion's sensor_kind onto a Sensor implementation.
// The runner builds one of these at startup with sensible defaults; the
// orchestrator looks up sensors by kind during the validate phase.
type Registry struct {
	mu      sync.RWMutex
	sensors map[string]Sensor
}

// DefaultRegistry returns the canonical mapping. Project-specific overrides
// can be layered on by calling Set after construction.
func DefaultRegistry() *Registry {
	r := &Registry{sensors: map[string]Sensor{}}
	r.Set("lint", NewShellSensor("lint", []string{"golangci-lint", "run", "./..."}))
	r.Set("typecheck", NewShellSensor("typecheck", []string{"go", "vet", "./..."}))
	r.Set("unit_test", NewShellSensor("unit_test", []string{"go", "test", "./..."}))
	r.Set("e2e_test", NewShellSensor("e2e_test", []string{"go", "test", "-tags=e2e", "./..."}))
	r.Set("build", NewShellSensor("build", []string{"go", "build", "./..."}))
	r.Set("screenshot", NewScreenshotSensor())
	// visual_diff is implemented by the screenshot sensor when the
	// criterion's sensor_spec.baseline_url is set. Registering both
	// kinds against the same struct lets a project encode the criterion
	// as either kind at the spec level — the runtime behaviour is
	// identical.
	r.Set("visual_diff", NewScreenshotSensor())
	// 'judge' and 'manual' are intentionally absent — orchestrator handles
	// the inferential judge directly, and 'manual' criteria are skipped
	// (the human is the sensor).
	return r
}

func (r *Registry) Set(kind string, s Sensor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sensors[kind] = s
}

func (r *Registry) For(kind string) Sensor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sensors[kind]
}

// Kinds returns the configured sensor kinds. Used by tests and diagnostics.
func (r *Registry) Kinds() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.sensors))
	for k := range r.sensors {
		out = append(out, k)
	}
	return out
}
