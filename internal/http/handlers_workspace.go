package http

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	"github.com/moltingduck/duckllo/internal/store"
)

// handleSetWorkspace records the runner's workspace metadata onto the run.
// The runner POSTs after provisioning (or re-attaching to) a Docker
// container so sensors that pull the bundle can see container_id and
// dev_url. Body shape mirrors workspace.Meta.
func (s *Server) handleSetWorkspace(w http.ResponseWriter, r *http.Request) {
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

	// We accept any JSON object as the meta payload to keep the runner
	// free to add new keys (Phase 2 -> Phase 3) without a server release.
	var raw map[string]any
	if err := decodeJSON(r, &raw); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	body, _ := json.Marshal(raw)
	if err := st.SetWorkspaceMeta(r.Context(), rid, body); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.publish(r, "run.workspace_set", map[string]any{"run_id": rid, "workspace_meta": raw})
	w.WriteHeader(http.StatusNoContent)
}
