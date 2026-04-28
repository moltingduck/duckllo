-- 005_verifications.sql
-- Typed sensor outputs (replaces testing_result/demo_gif_url), human visual
-- annotations (the correction signal), and a generic threaded comments table.

CREATE TABLE verifications (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id          UUID NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    iteration_id    UUID REFERENCES iterations(id) ON DELETE SET NULL,
    criterion_id    TEXT, -- references specs.acceptance_criteria[].id; NULL for ambient sensors
    kind            VARCHAR(50) NOT NULL,
    -- lint | typecheck | unit_test | e2e_test | build | screenshot | visual_diff | gif | judge | manual
    class           VARCHAR(50) NOT NULL,  -- computational | inferential | human
    direction       VARCHAR(50) NOT NULL DEFAULT 'feedback', -- feedforward | feedback
    status          VARCHAR(50) NOT NULL DEFAULT 'pending',  -- pass | fail | warn | skipped | pending
    summary         TEXT NOT NULL DEFAULT '',
    artifact_url    TEXT NOT NULL DEFAULT '',
    details_json    JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_verifications_run ON verifications(run_id);
CREATE INDEX idx_verifications_run_kind ON verifications(run_id, kind);

CREATE TABLE annotations (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    verification_id UUID NOT NULL REFERENCES verifications(id) ON DELETE CASCADE,
    author_id       UUID REFERENCES users(id),
    bbox            JSONB NOT NULL,            -- {x,y,w,h} image-relative
    body            TEXT NOT NULL DEFAULT '',
    verdict         VARCHAR(50) NOT NULL DEFAULT 'fix_required', -- fix_required | acceptable | nit
    resolved        BOOLEAN NOT NULL DEFAULT FALSE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_annotations_verification ON annotations(verification_id);

CREATE TABLE comments (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    target_kind     VARCHAR(50) NOT NULL, -- spec | plan | run | iteration | verification
    target_id       UUID NOT NULL,
    author_id       UUID REFERENCES users(id),
    body            TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_comments_target ON comments(target_kind, target_id);
CREATE INDEX idx_comments_project ON comments(project_id);
