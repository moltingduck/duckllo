-- 004_runs.sql
-- Runs, iterations, agent_sessions, and the work_queue table. Runners use
-- FOR UPDATE SKIP LOCKED on work_queue to claim work atomically; the lock
-- is released either explicitly on phase advance or by lock_expires_at
-- timing out (heartbeat keeps it fresh).

CREATE TABLE runs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    spec_id         UUID NOT NULL REFERENCES specs(id) ON DELETE CASCADE,
    plan_id         UUID NOT NULL REFERENCES plans(id) ON DELETE CASCADE,
    status          VARCHAR(50) NOT NULL DEFAULT 'queued',
    -- queued | planning | executing | validating | correcting | done | failed | aborted
    runner_id       VARCHAR(255),
    claimed_at      TIMESTAMPTZ,
    lock_expires_at TIMESTAMPTZ,
    workspace_meta  JSONB NOT NULL DEFAULT '{}'::jsonb,
    turn_budget     INTEGER NOT NULL DEFAULT 50,
    turns_used      INTEGER NOT NULL DEFAULT 0,
    token_usage     INTEGER NOT NULL DEFAULT 0,
    started_at      TIMESTAMPTZ,
    finished_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_runs_spec ON runs(spec_id);
CREATE INDEX idx_runs_status ON runs(status);

CREATE TABLE iterations (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id              UUID NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    idx                 INTEGER NOT NULL,
    phase               VARCHAR(50) NOT NULL, -- plan | execute | validate | correct
    agent_role          VARCHAR(50) NOT NULL, -- planner | executor | validator | corrector | reviewer
    provider            VARCHAR(50) NOT NULL DEFAULT 'anthropic',
    model               VARCHAR(255) NOT NULL DEFAULT '',
    summary             TEXT NOT NULL DEFAULT '',
    transcript_url      TEXT NOT NULL DEFAULT '',
    prompt_tokens       INTEGER NOT NULL DEFAULT 0,
    completion_tokens   INTEGER NOT NULL DEFAULT 0,
    status              VARCHAR(50) NOT NULL DEFAULT 'running', -- running | done | failed
    started_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at         TIMESTAMPTZ,
    UNIQUE (run_id, idx)
);
CREATE INDEX idx_iterations_run ON iterations(run_id);

CREATE TABLE agent_sessions (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id                  UUID NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    role                    VARCHAR(50) NOT NULL,
    provider                VARCHAR(50) NOT NULL DEFAULT 'anthropic',
    model                   VARCHAR(255) NOT NULL DEFAULT '',
    tool_allowlist          JSONB NOT NULL DEFAULT '[]'::jsonb,
    system_prompt_snapshot  TEXT NOT NULL DEFAULT '',
    idle_timeout_at         TIMESTAMPTZ,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_agent_sessions_run ON agent_sessions(run_id);

CREATE TABLE work_queue (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id          UUID NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    phase           VARCHAR(50) NOT NULL,
    status          VARCHAR(50) NOT NULL DEFAULT 'pending', -- pending | claimed | done | failed
    claimed_by      VARCHAR(255),
    claimed_at      TIMESTAMPTZ,
    lock_expires_at TIMESTAMPTZ,
    attempts        INTEGER NOT NULL DEFAULT 0,
    payload         JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_work_queue_status ON work_queue(status);
CREATE INDEX idx_work_queue_run ON work_queue(run_id);
