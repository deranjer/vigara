CREATE TABLE stack_git_settings (
 singleton BOOLEAN PRIMARY KEY DEFAULT true CHECK(singleton),
 remote TEXT NOT NULL, branch TEXT NOT NULL, version BIGINT NOT NULL DEFAULT 1,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE stack_bindings ADD COLUMN directory TEXT NOT NULL DEFAULT '';
ALTER TABLE stack_bindings ADD COLUMN display_name TEXT NOT NULL DEFAULT '';
