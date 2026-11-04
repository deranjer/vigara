CREATE TABLE agent_updates (
    agent_id uuid PRIMARY KEY REFERENCES agent_identities(id) ON DELETE CASCADE,
    automatic boolean NOT NULL DEFAULT false,
    requested_digest text NOT NULL DEFAULT '',
    generation bigint NOT NULL DEFAULT 0,
    status jsonb NOT NULL DEFAULT '{"state":"not_installed"}',
    reported_at timestamptz
);
