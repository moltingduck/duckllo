-- Project-scoped language preference for what the LLM agents reply in.
-- The runner orchestrator reads this from the bundle and injects a
-- "Respond in {language}" instruction into every phase's system prompt;
-- the suggest endpoint does the same. UI language is per-user and
-- handled client-side (localStorage); this column is only about model
-- output language.
--
-- Allowed values today: 'en' | 'zh-TW'. Stored as VARCHAR so adding a
-- new locale doesn't need a migration.
ALTER TABLE projects
    ADD COLUMN IF NOT EXISTS language VARCHAR(10) NOT NULL DEFAULT 'en';
