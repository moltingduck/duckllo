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

	var plan *models.Plan
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
		plan = p
	} else {
		p, err := st.LatestApprovedPlan(r.Context(), spec.ID)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusBadRequest, "spec has no approved plan; create+approve a plan first")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		plan = p
	}
	run, err := st.EnqueueRun(r.Context(), spec.ID, plan.ID, req.TurnBudget)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.publish(r, "run.queued", run)
	writeJSON(w, http.StatusCreated, run)
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
	if err := st.AbortRun(r.Context(), rid); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
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
	run, err := store.New(s.pool).AdvanceRun(r.Context(), rid, req.RunnerID, req.FromPhase, req.ToPhase, req.FinalStatus)
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
