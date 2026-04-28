package http

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/moltingduck/duckllo/internal/store"
)

type postCommentReq struct {
	TargetKind string `json:"target_kind"`
	TargetID   string `json:"target_id"`
	Body       string `json:"body"`
}

func (s *Server) handlePostComment(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r)
	p := projectFromCtx(r)
	if p == nil {
		writeError(w, http.StatusNotFound, "project not loaded")
		return
	}
	var req postCommentReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !validCommentTarget(req.TargetKind) {
		writeError(w, http.StatusBadRequest, "invalid target_kind")
		return
	}
	tid, err := uuid.Parse(req.TargetID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid target_id")
		return
	}
	var authorID *uuid.UUID
	if user != nil {
		authorID = &user.ID
	}
	c, err := store.New(s.pool).CreateComment(r.Context(), p.ID, authorID, req.TargetKind, tid, req.Body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.publish(r, "comment.posted", c)
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) handleListComments(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("target_kind")
	if !validCommentTarget(kind) {
		writeError(w, http.StatusBadRequest, "target_kind required")
		return
	}
	tid, err := uuid.Parse(r.URL.Query().Get("target_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "target_id required")
		return
	}
	out, err := store.New(s.pool).ListComments(r.Context(), kind, tid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func validCommentTarget(k string) bool {
	switch k {
	case "spec", "plan", "run", "iteration", "verification":
		return true
	}
	return false
}
