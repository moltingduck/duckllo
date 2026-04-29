package http

import (
	"net/http"

	"github.com/moltingduck/duckllo/internal/store"
)

// handleRecurringFailures surfaces the steering signal: criteria whose
// verifications keep failing across the last 30 days. The UI's steering
// page renders this as a "recurring failures" tab; clicking a row drops
// the user into a pre-populated rule-creation form so they can encode
// the pattern as a guide.
func (s *Server) handleRecurringFailures(w http.ResponseWriter, r *http.Request) {
	p := projectFromCtx(r)
	if p == nil {
		writeError(w, http.StatusNotFound, "project not loaded")
		return
	}
	out, err := store.New(s.pool).RecurringFailures(r.Context(), p.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}
