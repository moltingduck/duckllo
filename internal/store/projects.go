package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/moltingduck/duckllo/internal/models"
)

func (s *Store) CreateProject(ctx context.Context, name, description string, ownerID uuid.UUID) (*models.Project, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var p models.Project
	if err := tx.QueryRow(ctx, `
		INSERT INTO projects (name, description, owner_id)
		VALUES ($1, $2, $3)
		RETURNING id, name, COALESCE(description,''), owner_id, git_repo_url, settings, language, created_at, updated_at
	`, name, description, ownerID).Scan(&p.ID, &p.Name, &p.Description, &p.OwnerID, &p.GitRepoURL, &p.Settings, &p.Language, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}

	// Owner is automatically a product_manager.
	if _, err := tx.Exec(ctx, `
		INSERT INTO project_members (project_id, user_id, role)
		VALUES ($1, $2, 'product_manager')
		ON CONFLICT (project_id, user_id) DO NOTHING
	`, p.ID, ownerID); err != nil {
		return nil, err
	}

	// Auto-attach gin as product_manager so the CLAUDE.md non-negotiable holds.
	var ginID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM users WHERE username = 'gin'`).Scan(&ginID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// gin not yet bootstrapped; nothing to attach.
	case err != nil:
		return nil, err
	default:
		if _, err := tx.Exec(ctx, `
			INSERT INTO project_members (project_id, user_id, role)
			VALUES ($1, $2, 'product_manager')
			ON CONFLICT (project_id, user_id) DO NOTHING
		`, p.ID, ginID); err != nil {
			return nil, err
		}
	}

	// Seed a default topology so empty projects still let the runner start.
	if _, err := tx.Exec(ctx, `
		INSERT INTO topologies (project_id, name, description, default_guides, default_sensors)
		VALUES ($1, 'Generic web app', 'Default topology — replace with one tailored to your stack.',
		        '[]'::jsonb, '[]'::jsonb)
	`, p.ID); err != nil {
		return nil, err
	}

	return &p, tx.Commit(ctx)
}

func (s *Store) ProjectByID(ctx context.Context, id uuid.UUID) (*models.Project, error) {
	var p models.Project
	err := s.Pool.QueryRow(ctx, `
		SELECT id, name, COALESCE(description,''), owner_id, git_repo_url, settings, language, created_at, updated_at
		FROM projects WHERE id = $1
	`, id).Scan(&p.ID, &p.Name, &p.Description, &p.OwnerID, &p.GitRepoURL, &p.Settings, &p.Language, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) ListProjectsForUser(ctx context.Context, userID uuid.UUID) ([]models.Project, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT p.id, p.name, COALESCE(p.description,''), p.owner_id, p.git_repo_url, p.settings, p.created_at, p.updated_at
		FROM projects p
		JOIN project_members m ON m.project_id = p.id
		WHERE m.user_id = $1
		ORDER BY p.created_at DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []models.Project{}
	for rows.Next() {
		var p models.Project
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.OwnerID, &p.GitRepoURL, &p.Settings, &p.Language, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) UpdateProject(ctx context.Context, id uuid.UUID, name, description, gitRepo, language string) (*models.Project, error) {
	var p models.Project
	err := s.Pool.QueryRow(ctx, `
		UPDATE projects
		SET name = COALESCE(NULLIF($2,''), name),
		    description = COALESCE($3, description),
		    git_repo_url = COALESCE($4, git_repo_url),
		    language = COALESCE(NULLIF($5,''), language),
		    updated_at = NOW()
		WHERE id = $1
		RETURNING id, name, COALESCE(description,''), owner_id, git_repo_url, settings, language, created_at, updated_at
	`, id, name, description, gitRepo, language).Scan(&p.ID, &p.Name, &p.Description, &p.OwnerID, &p.GitRepoURL, &p.Settings, &p.Language, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) DeleteProject(ctx context.Context, id uuid.UUID) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM projects WHERE id = $1`, id)
	return err
}

func (s *Store) MemberRole(ctx context.Context, projectID, userID uuid.UUID) (string, error) {
	var role string
	err := s.Pool.QueryRow(ctx, `
		SELECT role FROM project_members WHERE project_id = $1 AND user_id = $2
	`, projectID, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return role, err
}

func (s *Store) AddProjectMember(ctx context.Context, projectID, userID uuid.UUID, role string) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, $3)
		ON CONFLICT (project_id, user_id) DO UPDATE SET role = EXCLUDED.role
	`, projectID, userID, role)
	return err
}

func (s *Store) RemoveProjectMember(ctx context.Context, projectID, userID uuid.UUID) error {
	// gin is a non-negotiable steward; refuse to remove.
	var username string
	if err := s.Pool.QueryRow(ctx, `SELECT username FROM users WHERE id = $1`, userID).Scan(&username); err != nil {
		return err
	}
	if username == "gin" {
		return errors.New("gin is the system steward and cannot be removed")
	}
	_, err := s.Pool.Exec(ctx, `DELETE FROM project_members WHERE project_id = $1 AND user_id = $2`, projectID, userID)
	return err
}

func (s *Store) ListProjectMembers(ctx context.Context, projectID uuid.UUID) ([]models.ProjectMember, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT m.project_id, m.user_id, m.role, m.created_at, u.username, COALESCE(u.display_name,'')
		FROM project_members m
		JOIN users u ON u.id = m.user_id
		WHERE m.project_id = $1
		ORDER BY m.role, u.username
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []models.ProjectMember{}
	for rows.Next() {
		var m models.ProjectMember
		if err := rows.Scan(&m.ProjectID, &m.UserID, &m.Role, &m.CreatedAt, &m.Username, &m.DisplayName); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
