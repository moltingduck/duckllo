-- 003_specs.sql
-- Specs + plans. Specs replace cards; each acceptance_criteria entry is a
-- typed sensor target so validation is structured rather than free-text.

CREATE TABLE specs (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id          UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    topology_id         UUID REFERENCES topologies(id) ON DELETE SET NULL,
    title               VARCHAR(500) NOT NULL,
    intent              TEXT NOT NULL DEFAULT '',
    priority            VARCHAR(50) NOT NULL DEFAULT 'medium',
    status              VARCHAR(50) NOT NULL DEFAULT 'draft',  -- draft|proposed|approved|running|validated|merged|rejected
    acceptance_criteria JSONB NOT NULL DEFAULT '[]'::jsonb,    -- [{id,text,sensor_kind,satisfied,last_verification_id,sensor_spec}]
    reference_assets    JSONB NOT NULL DEFAULT '[]'::jsonb,    -- [{kind,url,label}]
    affected_components JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_by          UUID REFERENCES users(id),
    assignee_id         UUID REFERENCES users(id),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_specs_project_status ON specs(project_id, status);

CREATE TABLE plans (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    spec_id             UUID NOT NULL REFERENCES specs(id) ON DELETE CASCADE,
    version             INTEGER NOT NULL,
    created_by_role     VARCHAR(50) NOT NULL DEFAULT 'planner', -- planner | human
    status              VARCHAR(50) NOT NULL DEFAULT 'draft',   -- draft | approved | superseded
    steps               JSONB NOT NULL DEFAULT '[]'::jsonb,     -- [{id,order,summary,files_touched,sensors_targeted,notes}]
    dag                 JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_by          UUID REFERENCES users(id),
    approved_by         UUID REFERENCES users(id),
    approved_at         TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (spec_id, version)
);
CREATE INDEX idx_plans_spec_status ON plans(spec_id, status);
