package http

import (
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/moltingduck/duckllo/internal/store"
)

type appendIterationReq struct {
	Phase         string `json:"phase"`
	AgentRole     string `json:"agent_role"`
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	Summary       string `json:"summary"`
	TranscriptURL string `json:"transcript_url"`
}

func (s *Server) handleAppendIteration(w http.ResponseWriter, r *http.Request) {
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

	var req appendIterationReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !validPhase(req.Phase) || req.AgentRole == "" {
		writeError(w, http.StatusBadRequest, "phase and agent_role required")
		return
	}
	if req.Provider == "" {
		req.Provider = "anthropic"
	}
	it, err := st.AppendIteration(r.Context(), run.ID, req.Phase, req.AgentRole, req.Provider, req.Model, req.Summary, req.TranscriptURL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.publish(r, "iteration.appended", it)
	writeJSON(w, http.StatusCreated, it)
}

type patchIterationReq struct {
	Summary          *string `json:"summary,omitempty"`
	PromptTokens     *int    `json:"prompt_tokens,omitempty"`
	CompletionTokens *int    `json:"completion_tokens,omitempty"`
	Status           *string `json:"status,omitempty"`
}

func (s *Server) handlePatchIteration(w http.ResponseWriter, r *http.Request) {
	iid, err := uuid.Parse(chiURLParam(r, "iterID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid iteration id")
		return
	}
	var req patchIterationReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	updated, err := store.New(s.pool).UpdateIteration(r.Context(), iid, store.IterationPatch{
		Summary: req.Summary, PromptTokens: req.PromptTokens, CompletionTokens: req.CompletionTokens, Status: req.Status,
	})
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "iteration not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.publish(r, "iteration.updated", updated)
	writeJSON(w, http.StatusOK, updated)
}
