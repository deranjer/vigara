CREATE TABLE agent_download_sessions (
    id UUID PRIMARY KEY REFERENCES agent_enrollment_tokens(id) ON DELETE CASCADE,
    device_name TEXT NOT NULL CHECK (length(device_name) BETWEEN 1 AND 120),
    target TEXT NOT NULL CHECK (target IN ('windows-amd64','linux-amd64','linux-arm64')),
    version TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'waiting' CHECK (status IN ('waiting','enrolled','canceled','expired')),
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
