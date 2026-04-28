package http

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func (s *Server) routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	r.Get("/api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Public auth surface.
	r.Post("/api/auth/register", s.handleRegister)
	r.Post("/api/auth/login", s.handleLogin)

	// Authenticated routes.
	r.Group(func(r chi.Router) {
		r.Use(s.authenticate)

		r.Get("/api/auth/me", s.handleMe)
		r.Post("/api/auth/logout", s.handleLogout)

		r.Get("/api/projects", s.handleListProjects)
		r.Post("/api/projects", s.handleCreateProject)

		r.Route("/api/projects/{projectID}", func(r chi.Router) {
			r.Use(s.requireProjectAccess)

			r.Get("/", s.handleGetProject)
			r.Patch("/", s.handlePatchProject)
			r.Delete("/", s.handleDeleteProject)

			r.Get("/members", s.handleListMembers)
			r.Post("/members", s.handleAddMember)
			r.Delete("/members/{userID}", s.handleRemoveMember)

			r.Get("/api-keys", s.handleListKeys)
			r.Post("/api-keys", s.handleCreateKey)
			r.Delete("/api-keys/{keyID}", s.handleDeleteKey)
		})
	})

	return r
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
