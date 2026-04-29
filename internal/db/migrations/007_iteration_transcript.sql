-- 007_iteration_transcript.sql
-- Iterations grow a transcript column so the full model conversation
-- (prompt + response, or whatever the runner chooses to capture) is
-- available for debugging without depending on transcript_url pointing
-- at a separate artifact. The pre-existing transcript_url field stays
-- and remains optional for cases where the runner would rather upload
-- the full text as a separate artifact.

ALTER TABLE iterations ADD COLUMN IF NOT EXISTS transcript TEXT NOT NULL DEFAULT '';
