CREATE TABLE mcp_oauth_clients (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    redirect_uris TEXT[] NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- OAuth connections have their own login session. Password changes and account
-- deletion revoke connections through this foreign key, including pending codes.
CREATE TABLE mcp_oauth_codes (
    code_hash BYTEA PRIMARY KEY,
    session_id UUID NOT NULL REFERENCES user_sessions(id) ON DELETE CASCADE,
    client_id TEXT NOT NULL REFERENCES mcp_oauth_clients(id) ON DELETE CASCADE,
    redirect_uri TEXT NOT NULL,
    challenge TEXT NOT NULL,
    scopes TEXT[] NOT NULL,
    resource TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX mcp_oauth_codes_expiry_idx ON mcp_oauth_codes(expires_at);

CREATE TABLE mcp_oauth_grants (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id UUID NOT NULL REFERENCES user_sessions(id) ON DELETE CASCADE,
    client_id TEXT NOT NULL REFERENCES mcp_oauth_clients(id) ON DELETE CASCADE,
    scopes TEXT[] NOT NULL,
    resource TEXT NOT NULL,
    access_hash BYTEA NOT NULL UNIQUE,
    access_expires_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX mcp_oauth_grants_session_idx ON mcp_oauth_grants(session_id);
CREATE INDEX mcp_oauth_grants_expiry_idx ON mcp_oauth_grants(expires_at);

-- Retain consumed refresh hashes until the grant expires to detect replay and
-- revoke the complete connection. Plaintext credentials are never persisted.
CREATE TABLE mcp_oauth_refresh_tokens (
    token_hash BYTEA PRIMARY KEY,
    grant_id UUID NOT NULL REFERENCES mcp_oauth_grants(id) ON DELETE CASCADE,
    used BOOLEAN NOT NULL DEFAULT false
);
CREATE INDEX mcp_oauth_refresh_grant_idx ON mcp_oauth_refresh_tokens(grant_id);
