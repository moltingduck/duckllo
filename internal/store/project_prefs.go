package store

import (
	"context"

	"github.com/google/uuid"
)

// ProjectPref is one user's UI preferences for one project. Returned by
// ListProjectPrefs joined with the project list so the UI gets order
// + pinned/archived + project metadata in a single round trip.
type ProjectPref struct {
	ProjectID uuid.UUID `json:"project_id"`
	Position  int       `json:"position"`
	Pinned    bool      `json:"pinned"`
	Archived  bool      `json:"archived"`
}

// ProjectSummary aggregates the counts the project bar shows as
// status badges below the project name + the full hover tooltip. All
// counts are derived from base tables — there's no denormalised
// summary table, so the values are always fresh.
type ProjectSummary struct {
	ProjectID       uuid.UUID      `json:"project_id"`
	SpecsByStatus   map[string]int `json:"specs_by_status"`
	RunsValidating  int            `json:"runs_validating"`  // parked, awaiting human review
	RunsCorrecting  int            `json:"runs_correcting"`  // fix-loop in flight
	RunsActive      int            `json:"runs_active"`      // any non-terminal status
	OpenAnnotations int            `json:"open_annotations"` // fix_required, not yet resolved
}

// GetProjectPrefs returns the user's prefs for a single project, with
// defaults filled in if no row exists yet (position=0, pinned=false,
// archived=false). Never returns a "not found" error — absence of a
// row IS the default.
func (s *Store) GetProjectPrefs(ctx context.Context, userID, projectID uuid.UUID) (ProjectPref, error) {
	pref := ProjectPref{ProjectID: projectID}
	err := s.Pool.QueryRow(ctx, `
		SELECT position, pinned, archived
		FROM user_project_prefs
		WHERE user_id = $1 AND project_id = $2
	`, userID, projectID).Scan(&pref.Position, &pref.Pinned, &pref.Archived)
	if err != nil {
		// pgx returns ErrNoRows; we treat absence as "use defaults" so
		// the caller can render an unconfigured project tile cleanly.
		return pref, nil //nolint:nilerr
	}
	return pref, nil
}

// ProjectWithPref is one row of "project + this user's prefs" returned
// in display order. Used by the project bar's /bar endpoint so a
// single query covers both the projection and the sort.
type ProjectWithPref struct {
	ProjectID   uuid.UUID `json:"project_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Position    int       `json:"position"`
	Pinned      bool      `json:"pinned"`
	Archived    bool      `json:"archived"`
}

// ListProjectsWithPrefs returns the user's projects in display order
// (pinned first, then by position, then by created_at as the
// tie-breaker for projects the user hasn't touched). Each row
// includes the project metadata + the user's prefs so the project
// bar's /bar endpoint can render in one round trip.
func (s *Store) ListProjectsWithPrefs(ctx context.Context, userID uuid.UUID) ([]ProjectWithPref, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT p.id, p.name, p.description,
		       COALESCE(upp.position, 0)     AS position,
		       COALESCE(upp.pinned,   FALSE) AS pinned,
		       COALESCE(upp.archived, FALSE) AS archived
		FROM project_members m
		JOIN projects p ON p.id = m.project_id
		LEFT JOIN user_project_prefs upp
		       ON upp.user_id = m.user_id AND upp.project_id = p.id
		WHERE m.user_id = $1
		ORDER BY pinned DESC, position, p.created_at
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProjectWithPref{}
	for rows.Next() {
		var p ProjectWithPref
		if err := rows.Scan(&p.ProjectID, &p.Name, &p.Description,
			&p.Position, &p.Pinned, &p.Archived); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetProjectPrefs upserts the (user, project) row. Caller passes a
// nil pointer for any field they don't want to change — the COALESCE
// in the SQL keeps the existing value (or the default for a new row).
func (s *Store) SetProjectPrefs(ctx context.Context, userID, projectID uuid.UUID, position *int, pinned, archived *bool) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO user_project_prefs (user_id, project_id, position, pinned, archived)
		VALUES ($1, $2, COALESCE($3, 0), COALESCE($4, FALSE), COALESCE($5, FALSE))
		ON CONFLICT (user_id, project_id) DO UPDATE SET
			position   = COALESCE($3, user_project_prefs.position),
			pinned     = COALESCE($4, user_project_prefs.pinned),
			archived   = COALESCE($5, user_project_prefs.archived),
			updated_at = NOW()
	`, userID, projectID, position, pinned, archived)
	return err
}

// ReorderProjects sets each project's position to its index in the
// supplied slice in one transaction. The UI calls this after a
// drag-and-drop. Pinned status / archived flags are preserved.
func (s *Store) ReorderProjects(ctx context.Context, userID uuid.UUID, projectIDs []uuid.UUID) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	for i, pid := range projectIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO user_project_prefs (user_id, project_id, position)
			VALUES ($1, $2, $3)
			ON CONFLICT (user_id, project_id) DO UPDATE SET
				position = EXCLUDED.position, updated_at = NOW()
		`, userID, pid, i); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ProjectSummaryFor computes the live counts the UI shows on the
// project bar. Two queries (specs grouped by status, runs grouped by
// status) plus one open-annotations count. The numbers are derived,
// not cached — the project bar is OK with this latency since the
// queries are tiny and indexed.
func (s *Store) ProjectSummaryFor(ctx context.Context, projectID uuid.UUID) (*ProjectSummary, error) {
	out := &ProjectSummary{ProjectID: projectID, SpecsByStatus: map[string]int{}}

	// Specs grouped by status.
	rows, err := s.Pool.Query(ctx, `
		SELECT status, COUNT(*) FROM specs WHERE project_id = $1 GROUP BY status
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, err
		}
		out.SpecsByStatus[status] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Runs by status — only count runs in non-terminal states for
	// "active". 'validating' and 'correcting' get their own buckets
	// because the project bar surfaces them as user-attention badges.
	runRows, err := s.Pool.Query(ctx, `
		SELECT r.status, COUNT(*)
		FROM runs r
		JOIN specs s ON s.id = r.spec_id
		WHERE s.project_id = $1
		GROUP BY r.status
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer runRows.Close()
	for runRows.Next() {
		var status string
		var n int
		if err := runRows.Scan(&status, &n); err != nil {
			return nil, err
		}
		switch status {
		case "validating":
			out.RunsValidating = n
			out.RunsActive += n
		case "correcting":
			out.RunsCorrecting = n
			out.RunsActive += n
		case "queued", "planning", "executing":
			out.RunsActive += n
		}
	}
	if err := runRows.Err(); err != nil {
		return nil, err
	}

	// Open fix_required annotations not yet resolved — these are the
	// "human said the screenshot is wrong, corrector hasn't acted yet"
	// signals.
	if err := s.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM annotations a
		JOIN verifications v ON v.id = a.verification_id
		JOIN runs r ON r.id = v.run_id
		JOIN specs s ON s.id = r.spec_id
		WHERE s.project_id = $1 AND a.verdict = 'fix_required' AND a.resolved = FALSE
	`, projectID).Scan(&out.OpenAnnotations); err != nil {
		return nil, err
	}

	return out, nil
}
