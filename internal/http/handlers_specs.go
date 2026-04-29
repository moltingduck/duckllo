package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/moltingduck/duckllo/internal/models"
	"github.com/moltingduck/duckllo/internal/store"
)

type createSpecReq struct {
	Title      string `json:"title"`
	Intent     string `json:"intent"`
	Priority   string `json:"priority"`
	TopologyID string `json:"topology_id,omitempty"`
}

type patchSpecReq struct {
	Title              *string                       `json:"title,omitempty"`
	Intent             *string                       `json:"intent,omitempty"`
	Priority           *string                       `json:"priority,omitempty"`
	Status             *string                       `json:"status,omitempty"`
	AssigneeID         *string                       `json:"assignee_id,omitempty"`
	AcceptanceCriteria *[]models.AcceptanceCriterion `json:"acceptance_criteria,omitempty"`
	ReferenceAssets    *[]models.ReferenceAsset      `json:"reference_assets,omitempty"`
	AffectedComponents *[]string                     `json:"affected_components,omitempty"`
}

func (s *Server) handleCreateSpec(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r)
	p := projectFromCtx(r)
	if user == nil || p == nil {
		writeError(w, http.StatusUnauthorized, "auth + project required")
		return
	}
	var req createSpecReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		writeError(w, http.StatusBadRequest, "title required")
		return
	}
	var topID *uuid.UUID
	if req.TopologyID != "" {
		t, err := uuid.Parse(req.TopologyID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid topology_id")
			return
		}
		topID = &t
	}
	st := store.New(s.pool)
	spec, err := st.CreateSpec(r.Context(), p.ID, user.ID, req.Title, req.Intent, req.Priority, topID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.publish(r, "spec.created", spec)
	writeJSON(w, http.StatusCreated, spec)
}

func (s *Server) handleListSpecs(w http.ResponseWriter, r *http.Request) {
	p := projectFromCtx(r)
	if p == nil {
		writeError(w, http.StatusNotFound, "project not loaded")
		return
	}
	specs, err := store.New(s.pool).ListSpecs(r.Context(), p.ID, r.URL.Query().Get("status"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, specs)
}

func (s *Server) handleGetSpec(w http.ResponseWriter, r *http.Request) {
	spec, ok := loadSpec(s, w, r)
	if !ok {
		return
	}
	plans, err := store.New(s.pool).ListPlansForSpec(r.Context(), spec.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"spec":  spec,
		"plans": plans,
	})
}

func (s *Server) handlePatchSpec(w http.ResponseWriter, r *http.Request) {
	spec, ok := loadSpec(s, w, r)
	if !ok {
		return
	}
	var req patchSpecReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Once a spec is approved (or any later state), its contract — intent,
	// acceptance criteria, reference assets, affected components — is
	// frozen. Otherwise an in-flight run would be evaluated against a
	// different target than the one it was started for. Title / priority /
	// assignee are organisational metadata and stay editable. The Web UI
	// hides the edit affordances past 'proposed' but the API has to enforce
	// it too.
	mutatesContract := req.Intent != nil || req.AcceptanceCriteria != nil ||
		req.ReferenceAssets != nil || req.AffectedComponents != nil
	if mutatesContract && !specContractEditable(spec.Status) {
		writeError(w, http.StatusConflict,
			"spec contract is frozen at status '"+spec.Status+"' — only draft or proposed specs can edit intent / criteria / assets / components")
		return
	}

	patch := store.SpecPatch{Title: req.Title, Intent: req.Intent, Priority: req.Priority, Status: req.Status}
	if req.AssigneeID != nil {
		if *req.AssigneeID == "" {
			patch.AssigneeID = nil
		} else {
			a, err := uuid.Parse(*req.AssigneeID)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid assignee_id")
				return
			}
			patch.AssigneeID = &a
		}
	}
	if req.AcceptanceCriteria != nil {
		body, _ := json.Marshal(*req.AcceptanceCriteria)
		patch.AcceptanceCriteria = body
	}
	if req.ReferenceAssets != nil {
		body, _ := json.Marshal(*req.ReferenceAssets)
		patch.ReferenceAssets = body
	}
	if req.AffectedComponents != nil {
		body, _ := json.Marshal(*req.AffectedComponents)
		patch.AffectedComponents = body
	}

	updated, err := store.New(s.pool).UpdateSpec(r.Context(), spec.ID, patch)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.publish(r, "spec.updated", updated)
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleAddCriterion(w http.ResponseWriter, r *http.Request) {
	spec, ok := loadSpec(s, w, r)
	if !ok {
		return
	}
	if !specContractEditable(spec.Status) {
		writeError(w, http.StatusConflict,
			"spec contract is frozen at status '"+spec.Status+"' — criteria can only be added in draft or proposed")
		return
	}
	var crit models.AcceptanceCriterion
	if err := decodeJSON(r, &crit); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if crit.Text == "" || crit.SensorKind == "" {
		writeError(w, http.StatusBadRequest, "text and sensor_kind are required")
		return
	}
	updated, err := store.New(s.pool).AppendCriterion(r.Context(), spec.ID, crit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.publish(r, "spec.criteria_changed", updated)
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleApproveSpec(w http.ResponseWriter, r *http.Request) {
	spec, ok := loadSpec(s, w, r)
	if !ok {
		return
	}
	// Empty acceptance_criteria → meaningless run. The validator would
	// post zero verifications, the judge wouldn't fire, and the spec
	// would auto-advance to validated with no human or sensor signal
	// it was actually correct. Block at the approval boundary so the
	// PM either adds criteria or rejects the spec outright.
	var crits []models.AcceptanceCriterion
	_ = json.Unmarshal(spec.AcceptanceCriteria, &crits)
	if len(crits) == 0 {
		writeError(w, http.StatusBadRequest,
			"spec has no acceptance criteria — add at least one before approving")
		return
	}

	st := store.New(s.pool)
	updated, err := st.UpdateSpec(r.Context(), spec.ID, store.SpecPatch{Status: ptr("approved")})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.publish(r, "spec.updated", updated)
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleRejectSpec(w http.ResponseWriter, r *http.Request) {
	spec, ok := loadSpec(s, w, r)
	if !ok {
		return
	}
	updated, err := store.New(s.pool).UpdateSpec(r.Context(), spec.ID, store.SpecPatch{Status: ptr("rejected")})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.publish(r, "spec.updated", updated)
	writeJSON(w, http.StatusOK, updated)
}

// loadSpec resolves the URL specID, verifies it belongs to the loaded
// project, and returns it. On any failure it writes the response and
// returns false.
func loadSpec(s *Server, w http.ResponseWriter, r *http.Request) (*models.Spec, bool) {
	p := projectFromCtx(r)
	if p == nil {
		writeError(w, http.StatusNotFound, "project not loaded")
		return nil, false
	}
	id, err := uuid.Parse(chiURLParam(r, "specID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid spec id")
		return nil, false
	}
	sp, err := store.New(s.pool).SpecByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) || (sp != nil && sp.ProjectID != p.ID) {
		writeError(w, http.StatusNotFound, "spec not found in this project")
		return nil, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	return sp, true
}

func ptr[T any](v T) *T { return &v }

// specContractEditable returns true when the spec status still allows the
// contract (intent, acceptance_criteria, reference_assets,
// affected_components) to be edited — i.e. before the PM signs off.
// 'approved' onward freezes the contract so an in-flight run can't be
// re-targeted underneath the runner.
func specContractEditable(status string) bool {
	return status == "draft" || status == "proposed"
}
