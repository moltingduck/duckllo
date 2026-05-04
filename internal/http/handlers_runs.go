package http

import (
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/moltingduck/duckllo/internal/models"
	"github.com/moltingduck/duckllo/internal/store"
)

type createRunReq struct {
	PlanID      string `json:"plan_id,omitempty"`
	TurnBudget  int    `json:"turn_budget,omitempty"`
}

func (s *Server) handleCreateRun(w http.ResponseWriter, r *http.Request) {
	spec, ok := loadSpec(s, w, r)
	if !ok {
		return
	}
	st := store.New(s.pool)

	var req createRunReq
	_ = decodeJSON(r, &req) // body optional

	var planPtr *uuid.UUID
	if req.PlanID != "" {
		pid, err := uuid.Parse(req.PlanID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid plan_id")
			return
		}
		p, err := st.PlanByID(r.Context(), pid)
		if err != nil {
			writeError(w, http.StatusNotFound, "plan not found")
			return
		}
		if p.SpecID != spec.ID {
			writeError(w, http.StatusBadRequest, "plan does not belong to this spec")
			return
		}
		if p.Status != "approved" {
			writeError(w, http.StatusBadRequest, "plan is not approved")
			return
		}
		planPtr = &p.ID
	} else if p, err := st.LatestApprovedPlan(r.Context(), spec.ID); err == nil {
		planPtr = &p.ID
	} else if !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// If still no plan, the run starts in 'plan' phase and the planner
	// agent will draft+approve a plan as its first iteration.
	run, err := st.EnqueueRun(r.Context(), spec.ID, planPtr, req.TurnBudget)
	if errors.Is(err, store.ErrSpecNotEnqueueable) {
		writeError(w, http.StatusBadRequest,
			"spec is not in 'approved' status — approve it first, or wait for the in-flight run to finish")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.publish(r, "run.queued", run)
	writeJSON(w, http.StatusCreated, run)
}

// handleListRunsForSpec returns every run for the spec — newest first.
// Used by the spec page's Runs timeline so a user can navigate back
// to past runs (and their captured screenshots / GIFs) without
// having to remember URLs.
func (s *Server) handleListRunsForSpec(w http.ResponseWriter, r *http.Request) {
	spec, ok := loadSpec(s, w, r)
	if !ok {
		return
	}
	runs, err := store.New(s.pool).ListRunsForSpec(r.Context(), spec.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	rid, err := uuid.Parse(chiURLParam(r, "runID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid run id")
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
	iterations, err := st.ListIterations(r.Context(), run.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"run":        run,
		"iterations": iterations,
	})
}

// handleCompleteRun is the human-side counterpart to the runner's
// /advance call: a project member parks-or-resolves a run from the UI
// without needing the original runner_id. Used to push past a
// validator that left the run in 'validating' awaiting review.
func (s *Server) handleCompleteRun(w http.ResponseWriter, r *http.Request) {
	rid, err := uuid.Parse(chiURLParam(r, "runID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid run id")
		return
	}
	st := store.New(s.pool)
	run, err := st.RunByID(r.Context(), rid)
	if err != nil {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	if !runBelongsToProject(s, r, run) {
		writeError(w, http.StatusNotFound, "run not in this project")
		return
	}
	updated, err := st.CompleteRunByHuman(r.Context(), rid)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusBadRequest, "run is already in a terminal state")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.publish(r, "run.advanced", updated)
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleAbortRun(w http.ResponseWriter, r *http.Request) {
	rid, err := uuid.Parse(chiURLParam(r, "runID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid run id")
		return
	}
	st := store.New(s.pool)
	run, err := st.RunByID(r.Context(), rid)
	if err != nil {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	if !runBelongsToProject(s, r, run) {
		writeError(w, http.StatusNotFound, "run not in this project")
		return
	}
	updated, err := st.AbortRun(r.Context(), rid)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusBadRequest, "run is already in a terminal state")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.publish(r, "run.advanced", updated)
	w.WriteHeader(http.StatusNoContent)
}

type claimReq struct {
	RunnerID string   `json:"runner_id"`
	Phases   []string `json:"phases,omitempty"`
}

func (s *Server) handleClaimWork(w http.ResponseWriter, r *http.Request) {
	// Restrict claim to API-key auth (i.e. agents). Web sessions cannot
	// hijack runs; this keeps the harness from being driven by an end user
	// accidentally.
	if projectRoleFromCtx(r) != "agent" {
		writeError(w, http.StatusForbidden, "claim is for project API keys only")
		return
	}
	var req claimReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.RunnerID) == "" {
		writeError(w, http.StatusBadRequest, "runner_id required")
		return
	}
	item, run, err := store.New(s.pool).ClaimWork(r.Context(), req.RunnerID, req.Phases)
	if errors.Is(err, store.ErrNotFound) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"work_item": item,
		"run":       run,
	})
}

type heartbeatReq struct {
	RunnerID string `json:"runner_id"`
}

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	rid, err := uuid.Parse(chiURLParam(r, "runID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid run id")
		return
	}
	var req heartbeatReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := store.New(s.pool).HeartbeatRun(r.Context(), rid, req.RunnerID); err != nil {
		writeError(w, http.StatusGone, "lock expired or runner mismatch")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type advanceReq struct {
	RunnerID    string `json:"runner_id"`
	FromPhase   string `json:"from_phase"`
	ToPhase     string `json:"to_phase,omitempty"`     // empty = no follow-up; only valid with FinalStatus
	FinalStatus string `json:"final_status,omitempty"` // done | failed | aborted | (empty)
	PlanID      string `json:"plan_id,omitempty"`      // planner uses this to bind the new plan atomically
}

func (s *Server) handleAdvanceRun(w http.ResponseWriter, r *http.Request) {
	rid, err := uuid.Parse(chiURLParam(r, "runID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid run id")
		return
	}
	var req advanceReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !validPhase(req.FromPhase) {
		writeError(w, http.StatusBadRequest, "invalid from_phase")
		return
	}
	if req.ToPhase != "" && !validPhase(req.ToPhase) {
		writeError(w, http.StatusBadRequest, "invalid to_phase")
		return
	}
	var planPtr *uuid.UUID
	if req.PlanID != "" {
		pid, err := uuid.Parse(req.PlanID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid plan_id")
			return
		}
		planPtr = &pid
	}
	run, err := store.New(s.pool).AdvanceRun(r.Context(), rid, req.RunnerID, req.FromPhase, req.ToPhase, req.FinalStatus, planPtr)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusGone, "run lock expired or runner mismatch")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.publish(r, "run.advanced", run)
	writeJSON(w, http.StatusOK, run)
}

func validPhase(p string) bool {
	switch p {
	case "plan", "execute", "validate", "correct":
		return true
	}
	return false
}

func runBelongsToProject(s *Server, r *http.Request, run *models.Run) bool {
	p := projectFromCtx(r)
	if p == nil {
		return false
	}
	sp, err := store.New(s.pool).SpecByID(r.Context(), run.SpecID)
	if err != nil {
		return false
	}
	return sp.ProjectID == p.ID
}
