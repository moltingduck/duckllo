package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/moltingduck/duckllo/internal/store"
)

type postVerificationReq struct {
	IterationID string                 `json:"iteration_id,omitempty"`
	CriterionID string                 `json:"criterion_id,omitempty"`
	Kind        string                 `json:"kind"`
	Class       string                 `json:"class"`
	Direction   string                 `json:"direction,omitempty"`
	Status      string                 `json:"status"`
	Summary     string                 `json:"summary"`
	ArtifactURL string                 `json:"artifact_url,omitempty"`
	Details     map[string]any         `json:"details,omitempty"`
}

// handlePostVerification accepts either application/json (no artifact) or
// multipart/form-data with field name "file" + "meta" containing the JSON
// body. The runner uses multipart for screenshots/GIFs.
func (s *Server) handlePostVerification(w http.ResponseWriter, r *http.Request) {
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

	var req postVerificationReq
	contentType := r.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(contentType, "multipart/form-data"):
		url, _, _, err := s.uploads.SaveMultipart(r, "file")
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		req.ArtifactURL = url
		if meta := r.FormValue("meta"); meta != "" {
			if err := json.Unmarshal([]byte(meta), &req); err != nil {
				writeError(w, http.StatusBadRequest, "meta json: "+err.Error())
				return
			}
			// re-set artifact url since unmarshal may have overwritten it
			if req.ArtifactURL == "" {
				req.ArtifactURL = url
			}
		}
	default:
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	if req.Kind == "" || req.Class == "" || req.Status == "" {
		writeError(w, http.StatusBadRequest, "kind, class, status required")
		return
	}

	in := store.VerificationInput{
		RunID: run.ID, CriterionID: req.CriterionID,
		Kind: req.Kind, Class: req.Class, Direction: req.Direction, Status: req.Status,
		Summary: req.Summary, ArtifactURL: req.ArtifactURL,
	}
	if req.IterationID != "" {
		iid, err := uuid.Parse(req.IterationID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid iteration_id")
			return
		}
		in.IterationID = &iid
	}
	if req.Details != nil {
		if b, err := json.Marshal(req.Details); err == nil {
			in.Details = b
		}
	}

	v, err := st.CreateVerification(r.Context(), in)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.publish(r, "verification.posted", v)
	writeJSON(w, http.StatusCreated, v)
}

type patchVerificationReq struct {
	Status  *string `json:"status,omitempty"`
	Summary *string `json:"summary,omitempty"`
}

func (s *Server) handlePatchVerification(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chiURLParam(r, "verID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid verification id")
		return
	}
	var req patchVerificationReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	v, err := store.New(s.pool).UpdateVerification(r.Context(), id, store.VerificationPatch{Status: req.Status, Summary: req.Summary})
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "verification not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.publish(r, "verification.updated", v)
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleListVerifications(w http.ResponseWriter, r *http.Request) {
	rid, err := uuid.Parse(chiURLParam(r, "runID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid run id")
		return
	}
	list, err := store.New(s.pool).ListVerificationsForRun(r.Context(), rid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

type addAnnotationReq struct {
	BBox    map[string]any `json:"bbox"`
	Body    string         `json:"body"`
	Verdict string         `json:"verdict"`
}

func (s *Server) handleAddAnnotation(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r)
	vid, err := uuid.Parse(chiURLParam(r, "verID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid verification id")
		return
	}
	var req addAnnotationReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	bboxJSON, _ := json.Marshal(req.BBox)
	in := store.AnnotationInput{
		VerificationID: vid, BBox: bboxJSON, Body: req.Body, Verdict: req.Verdict,
	}
	if user != nil {
		in.AuthorID = &user.ID
	}
	a, err := store.New(s.pool).CreateAnnotation(r.Context(), in)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.publish(r, "annotation.added", a)
	writeJSON(w, http.StatusCreated, a)
}

func (s *Server) handleListAnnotations(w http.ResponseWriter, r *http.Request) {
	vid, err := uuid.Parse(chiURLParam(r, "verID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid verification id")
		return
	}
	out, err := store.New(s.pool).ListAnnotations(r.Context(), vid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}
