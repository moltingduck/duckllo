-- Each harness rule can now target specific PEVC phases. NULL or
-- empty array = "applies to every phase" (the prior behaviour, so
-- existing rows keep working without a backfill). When set, only
-- phases listed in the array see the rule injected into their prompt.
-- Allowed values: 'plan' | 'execute' | 'validate' | 'correct'.
ALTER TABLE harness_rules
    ADD COLUMN IF NOT EXISTS phases TEXT[] NOT NULL DEFAULT '{}';

-- Used by ListEnabledRulesForPhase to short-circuit the array overlap
-- check on the hot path. Partial — only the all-phases default rows
-- (the most common case) need a fast lookup; the targeted rows fall
-- through to a small sequential scan which is fine.
CREATE INDEX IF NOT EXISTS harness_rules_all_phases_idx
    ON harness_rules (project_id, enabled)
    WHERE phases = '{}';
