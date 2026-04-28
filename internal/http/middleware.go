package http

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/moltingduck/duckllo/internal/auth"
	"github.com/moltingduck/duckllo/internal/models"
	"github.com/moltingduck/duckllo/internal/store"
)

type ctxKey int

const (
	ctxUser ctxKey = iota
	ctxProject
	ctxProjectRole
	ctxAPIKey
)

func userFromCtx(r *http.Request) *models.User {
	if v, ok := r.Context().Value(ctxUser).(*models.User); ok {
		return v
	}
	return nil
}

func projectFromCtx(r *http.Request) *models.Project {
	if v, ok := r.Context().Value(ctxProject).(*models.Project); ok {
		return v
	}
	return nil
}

func projectRoleFromCtx(r *http.Request) string {
	if v, ok := r.Context().Value(ctxProjectRole).(string); ok {
		return v
	}
	return ""
}

// authenticate resolves either a session UUID Bearer token (web UI) or a
// duckllo_-prefixed API key (runner / agents). Found principal lands in
// the request context; missing or invalid auth returns 401.
func (s *Server) authenticate(next http.Handler) http.Handler {
	st := store.New(s.pool)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			writeError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}

		ctx := r.Context()

		if strings.HasPrefix(token, auth.APIKeyPrefix) {
			prefix, err := auth.ParseAPIKeyPrefix(token)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "malformed api key")
				return
			}
			row, err := st.FindAPIKey(ctx, token, prefix, auth.CheckAPIKey)
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusUnauthorized, "invalid api key")
				return
			}
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			user, err := st.UserByID(ctx, row.UserID)
			if err != nil || user.Disabled {
				writeError(w, http.StatusUnauthorized, "owning user not available")
				return
			}
			_ = st.TouchAPIKey(ctx, row.ID)

			ctx = context.WithValue(ctx, ctxUser, user)
			ctx = context.WithValue(ctx, ctxAPIKey, row)
			// API keys are project-scoped: pre-load the project so route
			// handlers can short-circuit access checks.
			project, err := st.ProjectByID(ctx, row.ProjectID)
			if err == nil {
				ctx = context.WithValue(ctx, ctxProject, project)
				ctx = context.WithValue(ctx, ctxProjectRole, "agent")
			}
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		tok, err := uuid.Parse(token)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid session token")
			return
		}
		sess, err := st.SessionByToken(ctx, tok)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusUnauthorized, "session expired or unknown")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		user, err := st.UserByID(ctx, sess.UserID)
		if err != nil || user.Disabled {
			writeError(w, http.StatusUnauthorized, "session owner not available")
			return
		}
		ctx = context.WithValue(ctx, ctxUser, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireProjectAccess loads the project from the URL parameter and ensures
// the authenticated principal is a member or already locked to it via API
// key. Project + role are placed on the context.
func (s *Server) requireProjectAccess(next http.Handler) http.Handler {
	st := store.New(s.pool)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := userFromCtx(r)
		if user == nil {
			writeError(w, http.StatusUnauthorized, "auth required")
			return
		}
		ctx := r.Context()

		// API key path: project pre-loaded by authenticate.
		if existing := projectFromCtx(r); existing != nil {
			if pid := chi.URLParam(r, "projectID"); pid != "" && pid != existing.ID.String() {
				writeError(w, http.StatusForbidden, "api key scoped to another project")
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		pidStr := chi.URLParam(r, "projectID")
		pid, err := uuid.Parse(pidStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid project id")
			return
		}
		project, err := st.ProjectByID(ctx, pid)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		role, err := st.MemberRole(ctx, pid, user.ID)
		if errors.Is(err, store.ErrNotFound) {
			// Admins can read any project for support purposes; members otherwise required.
			if user.SystemRole != "admin" {
				writeError(w, http.StatusForbidden, "not a project member")
				return
			}
			role = "admin"
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		ctx = context.WithValue(ctx, ctxProject, project)
		ctx = context.WithValue(ctx, ctxProjectRole, role)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	if t := r.URL.Query().Get("token"); t != "" {
		// SSE EventSource cannot set headers; fall back to ?token= for it.
		return t
	}
	return ""
}
