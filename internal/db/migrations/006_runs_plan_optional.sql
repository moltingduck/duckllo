-- 006_runs_plan_optional.sql
-- Allow a Run to start without a pre-existing approved plan: the planner
-- agent will produce one as its first iteration. Once the planner approves
-- a new plan, the runner sets runs.plan_id to that plan via SetRunPlan.

ALTER TABLE runs ALTER COLUMN plan_id DROP NOT NULL;
