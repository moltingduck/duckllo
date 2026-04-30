package http

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/moltingduck/duckllo/internal/models"
	"github.com/moltingduck/duckllo/internal/runner/client"
	"github.com/moltingduck/duckllo/internal/runner/orchestrator"
	"github.com/moltingduck/duckllo/internal/store"
)

// handleBundle returns everything a runner needs to assemble its prompt
// for the given run+phase: the spec, its acceptance criteria, the active
// plan, all enabled harness rules for the project (filtered by topology
// when set on the spec), prior iterations, prior verifications, and any
// open fix_required annotations from humans (the correction signal).
//
// The runner performs *no* secondary fetches per turn — this is the
// "context pull" boundary. Keeping it consolidated lets us measure prompt
// size and rate-limit context as the model evolves.
func (s *Server) handleBundle(w http.ResponseWriter, r *http.Request) {
	rid, err := uuid.Parse(chiURLParam(r, "runID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid run id")
		return
	}
	st := store.New(s.pool)
	run, err := st.RunByID(r.Context(), rid)
	if errors.Is(err, store.ErrNotFound) || (run != nil && !runBelongsToProject(s, r, run)) {
		writeError(w, http.StatusNotFound, "run not found in this project")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	spec, err := st.SpecByID(r.Context(), run.SpecID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// plan_id may still be unbound if we're in the 'plan' phase pre-planner.
	var plan *models.Plan
	if run.PlanID != (uuid.UUID{}) {
		plan, err = st.PlanByID(r.Context(), run.PlanID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	// Filter rules by the current run phase if the runner asked for
	// one (e.g. ?phase=validate). Without a phase param we keep the
	// old behaviour of returning every enabled rule, so older runner
	// versions still work.
	phase := r.URL.Query().Get("phase")
	rules, err := st.ListEnabledRulesForPhase(r.Context(), spec.ProjectID, spec.TopologyID, phase)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	iterations, err := st.ListIterations(r.Context(), run.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	verifications, err := st.ListVerificationsForRun(r.Context(), run.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	annotations, err := st.ListOpenAnnotationsForRun(r.Context(), run.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, bundleResponse{
		Run:           run,
		Spec:          spec,
		Plan:          plan,
		HarnessRules:  rules,
		Iterations:    iterations,
		Verifications: verifications,
		OpenAnnotations: annotations,
	})
}

type bundleResponse struct {
	Run             *models.Run            `json:"run"`
	Spec            *models.Spec           `json:"spec"`
	Plan            *models.Plan           `json:"plan"`
	HarnessRules    []store.HarnessRule    `json:"harness_rules"`
	Iterations      []models.Iteration     `json:"iterations"`
	Verifications   []models.Verification  `json:"verifications"`
	OpenAnnotations []models.Annotation    `json:"open_annotations"`
}

// handleRunPreview returns the assembled prompt for a given (run,
// phase) as labeled segments — what the agent would see if a runner
// claimed this phase right now. Each segment carries source metadata
// and an editable URL so the UI can render "this paragraph came from
// harness rule X, click to edit" affordances. Mirrors the runner's
// real prompt assembly via orchestrator.PreviewFor so the preview
// can never drift from what the model actually sees.
func (s *Server) handleRunPreview(w http.ResponseWriter, r *http.Request) {
	rid, err := uuid.Parse(chiURLParam(r, "runID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid run id")
		return
	}
	phase := r.URL.Query().Get("phase")
	if !validPhase(phase) {
		writeError(w, http.StatusBadRequest, "phase query param required (plan|execute|validate|correct)")
		return
	}
	st := store.New(s.pool)
	run, err := st.RunByID(r.Context(), rid)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !runBelongsToProject(s, r, run) {
		writeError(w, http.StatusNotFound, "run not in this project")
		return
	}
	spec, err := st.SpecByID(r.Context(), run.SpecID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var plan *models.Plan
	if run.PlanID != (uuid.UUID{}) {
		plan, _ = st.PlanByID(r.Context(), run.PlanID)
	}
	rules, err := st.ListEnabledRulesForPhase(r.Context(), spec.ProjectID, spec.TopologyID, phase)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	iterations, _ := st.ListIterations(r.Context(), run.ID)
	verifications, _ := st.ListVerificationsForRun(r.Context(), run.ID)
	openAnnos, _ := st.ListOpenAnnotationsForRun(r.Context(), run.ID)

	bundle := bundleToClient(run, spec, plan, rules, iterations, verifications, openAnnos)
	preview := orchestrator.PreviewFor(phase, bundle)
	writeJSON(w, http.StatusOK, preview)
}

// bundleToClient converts the server-side bundle (store + models
// types) into the runner-side client.Bundle that orchestrator's
// prompt assembly consumes. JSON round-trip is the laziest correct
// option — the wire shapes are designed to match — and it's cheap
// since this is a UI-driven preview, not a hot path.
func bundleToClient(run *models.Run, spec *models.Spec, plan *models.Plan,
	rules []store.HarnessRule, iters []models.Iteration,
	verifs []models.Verification, annos []models.Annotation) *client.Bundle {
	wire := bundleResponse{
		Run: run, Spec: spec, Plan: plan,
		HarnessRules: rules, Iterations: iters,
		Verifications: verifs, OpenAnnotations: annos,
	}
	raw, _ := json.Marshal(wire)
	var b client.Bundle
	_ = json.Unmarshal(raw, &b)
	return &b
}
