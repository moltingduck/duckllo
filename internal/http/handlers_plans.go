package http

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/moltingduck/duckllo/internal/models"
	"github.com/moltingduck/duckllo/internal/store"
)

type createPlanReq struct {
	CreatedByRole string             `json:"created_by_role"` // planner | human
	Steps         []models.PlanStep  `json:"steps"`
	DAG           []map[string]any   `json:"dag,omitempty"`
}

type patchPlanReq struct {
	Steps *[]models.PlanStep `json:"steps,omitempty"`
	DAG   *[]map[string]any  `json:"dag,omitempty"`
}

func (s *Server) handleCreatePlan(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r)
	spec, ok := loadSpec(s, w, r)
	if !ok {
		return
	}
	var req createPlanReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.CreatedByRole != "planner" && req.CreatedByRole != "human" {
		req.CreatedByRole = "planner"
	}
	stepsJSON, _ := json.Marshal(req.Steps)
	dagJSON, _ := json.Marshal(req.DAG)
	plan, err := store.New(s.pool).CreatePlan(r.Context(), spec.ID, &user.ID, req.CreatedByRole, stepsJSON, dagJSON)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, plan)
}

func (s *Server) handlePatchPlan(w http.ResponseWriter, r *http.Request) {
	planID, err := uuid.Parse(chiURLParam(r, "planID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid plan id")
		return
	}
	st := store.New(s.pool)
	existing, err := st.PlanByID(r.Context(), planID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "plan not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !planBelongsToProject(s, r, existing) {
		writeError(w, http.StatusNotFound, "plan not in this project")
		return
	}

	var req patchPlanReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var stepsJSON, dagJSON []byte
	if req.Steps != nil {
		stepsJSON, _ = json.Marshal(*req.Steps)
	}
	if req.DAG != nil {
		dagJSON, _ = json.Marshal(*req.DAG)
	}
	updated, err := st.UpdatePlanSteps(r.Context(), planID, stepsJSON, dagJSON)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleApprovePlan(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r)
	planID, err := uuid.Parse(chiURLParam(r, "planID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid plan id")
		return
	}
	st := store.New(s.pool)
	existing, err := st.PlanByID(r.Context(), planID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "plan not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !planBelongsToProject(s, r, existing) {
		writeError(w, http.StatusNotFound, "plan not in this project")
		return
	}
	if !canEditProject(projectRoleFromCtx(r)) {
		writeError(w, http.StatusForbidden, "only product managers can approve a plan")
		return
	}
	updated, err := st.ApprovePlan(r.Context(), planID, user.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func planBelongsToProject(s *Server, r *http.Request, plan *models.Plan) bool {
	p := projectFromCtx(r)
	if p == nil {
		return false
	}
	sp, err := store.New(s.pool).SpecByID(r.Context(), plan.SpecID)
	if err != nil {
		return false
	}
	return sp.ProjectID == p.ID
}
