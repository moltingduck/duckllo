package http

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/moltingduck/duckllo/internal/store"
)

type createTopologyReq struct {
	Name           string         `json:"name"`
	Description    string         `json:"description"`
	DefaultGuides  []any          `json:"default_guides,omitempty"`
	DefaultSensors []any          `json:"default_sensors,omitempty"`
}

func (s *Server) handleListTopologies(w http.ResponseWriter, r *http.Request) {
	p := projectFromCtx(r)
	out, err := store.New(s.pool).ListTopologies(r.Context(), p.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateTopology(w http.ResponseWriter, r *http.Request) {
	if !canEditProject(projectRoleFromCtx(r)) {
		writeError(w, http.StatusForbidden, "only product managers can create topologies")
		return
	}
	p := projectFromCtx(r)
	var req createTopologyReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	g, _ := json.Marshal(req.DefaultGuides)
	se, _ := json.Marshal(req.DefaultSensors)
	t, err := store.New(s.pool).CreateTopology(r.Context(), p.ID, req.Name, req.Description, g, se)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

type createRuleReq struct {
	TopologyID string   `json:"topology_id,omitempty"`
	Kind       string   `json:"kind"`
	Name       string   `json:"name"`
	Body       string   `json:"body"`
	Phases     []string `json:"phases,omitempty"`
}

func (s *Server) handleListRules(w http.ResponseWriter, r *http.Request) {
	p := projectFromCtx(r)
	var topID *uuid.UUID
	if t := r.URL.Query().Get("topology_id"); t != "" {
		parsed, err := uuid.Parse(t)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid topology_id")
			return
		}
		topID = &parsed
	}
	out, err := store.New(s.pool).ListEnabledRules(r.Context(), p.ID, topID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateRule(w http.ResponseWriter, r *http.Request) {
	if !canEditProject(projectRoleFromCtx(r)) {
		writeError(w, http.StatusForbidden, "only product managers can create rules")
		return
	}
	p := projectFromCtx(r)
	var req createRuleReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Kind == "" || req.Name == "" || req.Body == "" {
		writeError(w, http.StatusBadRequest, "kind, name, body required")
		return
	}
	var topID *uuid.UUID
	if req.TopologyID != "" {
		parsed, err := uuid.Parse(req.TopologyID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid topology_id")
			return
		}
		topID = &parsed
	}
	if err := validatePhases(req.Phases); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rule, err := store.New(s.pool).CreateRule(r.Context(), p.ID, topID, req.Kind, req.Name, req.Body, req.Phases)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, rule)
}

type patchRuleReq struct {
	Body    *string   `json:"body,omitempty"`
	Enabled *bool     `json:"enabled,omitempty"`
	Phases  *[]string `json:"phases,omitempty"`
}

// validatePhases enforces the canonical PEVC phase set on writes.
// Empty slice (or nil) is fine — that's the "applies to all phases"
// default. Anything else has to be one of the four real phase names
// the runner emits, otherwise the rule would be silently dead.
func validatePhases(ph []string) error {
	for _, p := range ph {
		switch p {
		case "plan", "execute", "validate", "correct":
		default:
			return errors.New("phases entries must be one of: plan, execute, validate, correct (got " + p + ")")
		}
	}
	return nil
}

func (s *Server) handlePatchRule(w http.ResponseWriter, r *http.Request) {
	if !canEditProject(projectRoleFromCtx(r)) {
		writeError(w, http.StatusForbidden, "only product managers can edit rules")
		return
	}
	id, err := uuid.Parse(chiURLParam(r, "ruleID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid rule id")
		return
	}
	var req patchRuleReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Phases != nil {
		if err := validatePhases(*req.Phases); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	rule, err := store.New(s.pool).UpdateRule(r.Context(), id, req.Body, req.Enabled, req.Phases)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rule)
}

func (s *Server) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	if !canEditProject(projectRoleFromCtx(r)) {
		writeError(w, http.StatusForbidden, "only product managers can delete rules")
		return
	}
	id, err := uuid.Parse(chiURLParam(r, "ruleID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid rule id")
		return
	}
	if err := store.New(s.pool).DeleteRule(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
