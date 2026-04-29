package http

import (
	"net/http"
	"time"

	"github.com/moltingduck/duckllo/internal/store"
	"github.com/moltingduck/duckllo/internal/version"
)

type statusResp struct {
	Version        string `json:"version"`
	SchemaVersion  string `json:"schema_version"`
	DBReachable    bool   `json:"db_reachable"`
	NeedsFirstUser bool   `json:"needs_first_user"`
	GinPresent     bool   `json:"gin_present"`
	UptimeSeconds  int64  `json:"uptime_seconds"`
}

// handleStatus returns enough state for an unauthenticated client to decide
// whether to show first-time-setup, the login form, or just continue.
// Intentionally tolerant: a partial DB failure still returns 200 with
// db_reachable=false so the UI can render a degraded-state banner.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	resp := statusResp{
		Version:       version.Version,
		UptimeSeconds: int64(time.Since(version.StartedAt).Seconds()),
	}

	st := store.New(s.pool)
	if err := s.pool.Ping(r.Context()); err == nil {
		resp.DBReachable = true
		if v, err := st.LatestSchemaVersion(r.Context()); err == nil {
			resp.SchemaVersion = v
		}
		if n, err := st.CountUsers(r.Context()); err == nil {
			resp.NeedsFirstUser = n == 0
		}
		if g, err := st.GinPresent(r.Context()); err == nil {
			resp.GinPresent = g
		}
	}

	writeJSON(w, http.StatusOK, resp)
}
