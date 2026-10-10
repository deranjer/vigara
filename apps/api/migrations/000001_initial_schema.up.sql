CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE devices (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    display_name TEXT NOT NULL,
    platform_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX devices_platform_id_unique ON devices (platform_id) WHERE platform_id IS NOT NULL;

CREATE TABLE agent_identities (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    device_id UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    credential_hash BYTEA NOT NULL UNIQUE,
    protocol_version TEXT NOT NULL,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
