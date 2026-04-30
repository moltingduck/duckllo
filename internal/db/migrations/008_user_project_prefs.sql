-- Per-user, per-project UI preferences for the top project bar.
-- Position is the user's drag-ordered slot (lower = leftmost). Pinned
-- projects always sort before non-pinned. Archived projects hide from
-- the bar's main visible row but stay accessible via the overflow "..."
-- menu. Row missing for a (user, project) pair = defaults: position 0,
-- not pinned, not archived.

CREATE TABLE IF NOT EXISTS user_project_prefs (
    user_id    UUID NOT NULL REFERENCES users(id)    ON DELETE CASCADE,
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    position   INTEGER     NOT NULL DEFAULT 0,
    pinned     BOOLEAN     NOT NULL DEFAULT FALSE,
    archived   BOOLEAN     NOT NULL DEFAULT FALSE,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, project_id)
);

-- Most reads are "give me one user's visible projects in order" — index
-- it. Partial-index the archived=false case because that's the hot
-- path; the overflow "..." view that shows archived ones is rare.
CREATE INDEX IF NOT EXISTS user_project_prefs_user_visible_idx
    ON user_project_prefs (user_id, pinned DESC, position, project_id)
    WHERE archived = FALSE;
