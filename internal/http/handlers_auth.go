package http

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/moltingduck/duckllo/internal/auth"
	"github.com/moltingduck/duckllo/internal/store"
)

type registerReq struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type sessionResp struct {
	Token     uuid.UUID `json:"token"`
	UserID    uuid.UUID `json:"user_id"`
	Username  string    `json:"username"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || len(req.Password) < 6 {
		writeError(w, http.StatusBadRequest, "username and password (>=6 chars) required")
		return
	}

	st := store.New(s.pool)

	// First user becomes admin and seeds gin-as-steward semantics.
	count, err := st.CountUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	role := "user"
	if count == 0 {
		role = "admin"
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	user, err := st.CreateUser(r.Context(), req.Username, hash, req.DisplayName, role)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sess, err := st.CreateSession(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, sessionResp{
		Token: sess.Token, UserID: user.ID, Username: user.Username, ExpiresAt: sess.ExpiresAt,
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	st := store.New(s.pool)
	user, err := st.UserByUsername(r.Context(), strings.TrimSpace(req.Username))
	if errors.Is(err, store.ErrNotFound) || (user != nil && user.Disabled) {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !auth.CheckPassword(user.PasswordHash, req.Password) {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	sess, err := st.CreateSession(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sessionResp{
		Token: sess.Token, UserID: user.ID, Username: user.Username, ExpiresAt: sess.ExpiresAt,
	})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "auth required")
		return
	}
	writeJSON(w, http.StatusOK, user)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	tok, err := uuid.Parse(bearerToken(r))
	if err == nil {
		_ = store.New(s.pool).DeleteSession(r.Context(), tok)
	}
	w.WriteHeader(http.StatusNoContent)
}
