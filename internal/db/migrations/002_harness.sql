-- 002_harness.sql
-- Topologies (Ashby's Law variety reducer) and harness rules (steering loop
-- editable from the Web UI). Both are project-scoped guides that are baked
-- into the runner's per-iteration prompt.

CREATE TABLE topologies (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name            VARCHAR(255) NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    default_guides  JSONB NOT NULL DEFAULT '[]'::jsonb,
    default_sensors JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_topologies_project ON topologies(project_id);

CREATE TABLE harness_rules (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    topology_id     UUID REFERENCES topologies(id) ON DELETE SET NULL,
    kind            VARCHAR(50) NOT NULL, -- agents_md | skill | lint_config | architectural_rule | judge_prompt
    name            VARCHAR(255) NOT NULL,
    body            TEXT NOT NULL,
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_harness_rules_project ON harness_rules(project_id);
CREATE INDEX idx_harness_rules_topology ON harness_rules(topology_id);
