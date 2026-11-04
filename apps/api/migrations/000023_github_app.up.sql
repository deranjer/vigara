ALTER TABLE stack_git_settings ADD COLUMN auth_mode TEXT NOT NULL DEFAULT 'server'
    CHECK (auth_mode IN ('server', 'github-app'));

CREATE TABLE stack_github_app (
    singleton BOOLEAN PRIMARY KEY DEFAULT true CHECK (singleton),
    app_id BIGINT NOT NULL,
    slug TEXT NOT NULL,
    installation_id BIGINT NOT NULL,
    remote TEXT NOT NULL,
    branch TEXT NOT NULL,
    credentials BYTEA NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE stack_github_app_setups (
    state_hash TEXT PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    session_hash TEXT NOT NULL,
    phase TEXT NOT NULL CHECK (phase IN ('register', 'converting', 'install', 'finishing')),
    remote TEXT NOT NULL,
    branch TEXT NOT NULL,
    credentials BYTEA,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX stack_github_app_setups_expiry ON stack_github_app_setups(expires_at);
