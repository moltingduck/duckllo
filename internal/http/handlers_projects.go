package http

import (
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/moltingduck/duckllo/internal/auth"
	"github.com/moltingduck/duckllo/internal/store"
)

type createProjectReq struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type patchProjectReq struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	GitRepoURL  *string `json:"git_repo_url,omitempty"`
	Language    *string `json:"language,omitempty"`
}

// validProjectLanguage is the allow-list for projects.language. The
// runner orchestrator and the suggest endpoint inject a "Respond in
// {language}" directive, so anything we accept here has to be a label
// the model actually understands. Add to this when shipping new locales.
func validProjectLanguage(s string) bool {
	switch s {
	case "en", "zh-TW":
		return true
	}
	return false
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "auth required")
		return
	}
	var req createProjectReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name required")
		return
	}
	st := store.New(s.pool)
	p, err := st.CreateProject(r.Context(), req.Name, req.Description, user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "auth required")
		return
	}
	projects, err := store.New(s.pool).ListProjectsForUser(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, projects)
}

// handleProjectBar is the project-bar's "fetch everything I need to
// render" call. Returns each project the user belongs to plus the
// user's prefs (position / pinned / archived) plus a per-project
// summary (counts of specs by status, runs awaiting review, etc.).
// One round trip on initial render; SSE-driven refreshes use the
// per-project /summary endpoint below.
func (s *Server) handleProjectBar(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "auth required")
		return
	}
	st := store.New(s.pool)
	projects, err := st.ListProjectsWithPrefs(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type tile struct {
		Pref    store.ProjectPref     `json:"pref"`
		Name    string                `json:"name"`
		Desc    string                `json:"description"`
		Summary *store.ProjectSummary `json:"summary"`
	}
	tiles := make([]tile, 0, len(projects))
	for _, p := range projects {
		sum, err := st.ProjectSummaryFor(r.Context(), p.ProjectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		tiles = append(tiles, tile{
			Pref: store.ProjectPref{
				ProjectID: p.ProjectID, Position: p.Position,
				Pinned: p.Pinned, Archived: p.Archived,
			},
			Name:    p.Name,
			Desc:    p.Description,
			Summary: sum,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": tiles})
}

// handleProjectSummary returns the live counts for one project. The
// project bar calls this when it sees an SSE event on that project's
// channel — re-issuing the bar fetch for every event would scan every
// project every time, this is the targeted refresh path.
func (s *Server) handleProjectSummary(w http.ResponseWriter, r *http.Request) {
	p := projectFromCtx(r)
	if p == nil {
		writeError(w, http.StatusNotFound, "project not loaded")
		return
	}
	sum, err := store.New(s.pool).ProjectSummaryFor(r.Context(), p.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

type patchPrefsReq struct {
	Pinned   *bool `json:"pinned,omitempty"`
	Archived *bool `json:"archived,omitempty"`
}

// handlePatchProjectPrefs flips this user's pin/archive state for the
// project. Doesn't touch position — that goes through reorder so the
// drag operation can write all positions in one transaction.
func (s *Server) handlePatchProjectPrefs(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r)
	p := projectFromCtx(r)
	if user == nil || p == nil {
		writeError(w, http.StatusUnauthorized, "auth + project required")
		return
	}
	var req patchPrefsReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := store.New(s.pool).SetProjectPrefs(r.Context(), user.ID, p.ID, nil, req.Pinned, req.Archived); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type reorderReq struct {
	ProjectIDs []string `json:"project_ids"`
}

// handleReorderProjects writes each id's index as its position in one
// transaction. The UI sends the post-drag order; the server doesn't
// validate that the user has access to every id (the FK + the per-row
// user_id constraint will happily ignore foreign IDs because no row
// will be inserted/updated for a (this user, foreign project) pair).
// Caller is responsible for sending only IDs they're a member of.
func (s *Server) handleReorderProjects(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "auth required")
		return
	}
	var req reorderReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ids := make([]uuid.UUID, 0, len(req.ProjectIDs))
	for _, s := range req.ProjectIDs {
		id, err := uuid.Parse(s)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid project id "+s)
			return
		}
		ids = append(ids, id)
	}
	if err := store.New(s.pool).ReorderProjects(r.Context(), user.ID, ids); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	p := projectFromCtx(r)
	if p == nil {
		writeError(w, http.StatusNotFound, "project not loaded")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handlePatchProject(w http.ResponseWriter, r *http.Request) {
	p := projectFromCtx(r)
	if p == nil {
		writeError(w, http.StatusNotFound, "project not loaded")
		return
	}
	if !canEditProject(projectRoleFromCtx(r)) {
		writeError(w, http.StatusForbidden, "only owners or product managers can edit a project")
		return
	}
	var req patchProjectReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	name, desc, repo, lang := "", "", "", ""
	if req.Name != nil {
		name = *req.Name
	}
	if req.Description != nil {
		desc = *req.Description
	}
	if req.GitRepoURL != nil {
		repo = *req.GitRepoURL
	}
	if req.Language != nil {
		if !validProjectLanguage(*req.Language) {
			writeError(w, http.StatusBadRequest, "language must be 'en' or 'zh-TW'")
			return
		}
		lang = *req.Language
	}
	updated, err := store.New(s.pool).UpdateProject(r.Context(), p.ID, name, desc, repo, lang)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	p := projectFromCtx(r)
	if p == nil {
		writeError(w, http.StatusNotFound, "project not loaded")
		return
	}
	user := userFromCtx(r)
	if user == nil || (p.OwnerID != user.ID && user.SystemRole != "admin") {
		writeError(w, http.StatusForbidden, "only the owner or an admin can delete a project")
		return
	}
	if err := store.New(s.pool).DeleteProject(r.Context(), p.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request) {
	p := projectFromCtx(r)
	if p == nil {
		writeError(w, http.StatusNotFound, "project not loaded")
		return
	}
	members, err := store.New(s.pool).ListProjectMembers(r.Context(), p.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, members)
}

type addMemberReq struct {
	Username string `json:"username"`
	UserID   string `json:"user_id"`
	Role     string `json:"role"`
}

func (s *Server) handleAddMember(w http.ResponseWriter, r *http.Request) {
	if !canEditProject(projectRoleFromCtx(r)) {
		writeError(w, http.StatusForbidden, "only product managers can add members")
		return
	}
	p := projectFromCtx(r)
	var req addMemberReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Role == "" {
		req.Role = "developer"
	}
	st := store.New(s.pool)

	var uid uuid.UUID
	if req.UserID != "" {
		parsed, err := uuid.Parse(req.UserID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid user_id")
			return
		}
		uid = parsed
	} else {
		u, err := st.UserByUsername(r.Context(), req.Username)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		uid = u.ID
	}
	if err := st.AddProjectMember(r.Context(), p.ID, uid, req.Role); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	if !canEditProject(projectRoleFromCtx(r)) {
		writeError(w, http.StatusForbidden, "only product managers can remove members")
		return
	}
	p := projectFromCtx(r)
	uidStr := chiURLParam(r, "userID")
	uid, err := uuid.Parse(uidStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	if err := store.New(s.pool).RemoveProjectMember(r.Context(), p.ID, uid); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type createKeyReq struct {
	Label       string   `json:"label"`
	Permissions []string `json:"permissions"`
}

func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request) {
	if !canEditProject(projectRoleFromCtx(r)) {
		writeError(w, http.StatusForbidden, "only product managers can list api keys")
		return
	}
	p := projectFromCtx(r)
	keys, err := store.New(s.pool).ListAPIKeysForProject(r.Context(), p.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, keys)
}

func (s *Server) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	if !canEditProject(projectRoleFromCtx(r)) {
		writeError(w, http.StatusForbidden, "only product managers can mint api keys")
		return
	}
	user := userFromCtx(r)
	p := projectFromCtx(r)
	var req createKeyReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(req.Permissions) == 0 {
		req.Permissions = []string{"read", "write"}
	}
	plain, prefix, hash, err := auth.MintAPIKey()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	permsJSON, _ := jsonMarshal(req.Permissions)
	rec, err := store.New(s.pool).CreateAPIKey(r.Context(), user.ID, p.ID, req.Label, prefix, hash, permsJSON)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"api_key": rec,
		"plain":   plain,
		"warning": "store this token now — it will not be shown again",
	})
}

func (s *Server) handleDeleteKey(w http.ResponseWriter, r *http.Request) {
	if !canEditProject(projectRoleFromCtx(r)) {
		writeError(w, http.StatusForbidden, "only product managers can delete api keys")
		return
	}
	p := projectFromCtx(r)
	id, err := uuid.Parse(chiURLParam(r, "keyID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid key id")
		return
	}
	if err := store.New(s.pool).DeleteAPIKey(r.Context(), id, p.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "key not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func canEditProject(role string) bool {
	switch role {
	case "product_manager", "owner", "admin":
		return true
	}
	return false
}
