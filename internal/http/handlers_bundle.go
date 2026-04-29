package http

import (
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/moltingduck/duckllo/internal/models"
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

	rules, err := st.ListEnabledRules(r.Context(), spec.ProjectID, spec.TopologyID)
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
