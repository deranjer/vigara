ALTER TABLE agent_identities
    ADD COLUMN platform TEXT NOT NULL DEFAULT '',
    ADD COLUMN architecture TEXT NOT NULL DEFAULT '',
    ADD COLUMN agent_version TEXT NOT NULL DEFAULT '',
    ADD COLUMN capabilities JSONB NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN allowed_capabilities TEXT[] NOT NULL DEFAULT '{}';

CREATE TABLE agent_enrollment_tokens (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash BYTEA NOT NULL UNIQUE,
    capabilities TEXT[] NOT NULL DEFAULT '{}',
    expires_at TIMESTAMPTZ NOT NULL,
    used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE agent_reports (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id UUID NOT NULL REFERENCES agent_identities(id) ON DELETE CASCADE,
    report_id TEXT NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(agent_id, report_id)
);

CREATE INDEX agent_reports_created_at_idx ON agent_reports (created_at);
