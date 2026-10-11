CREATE TABLE android_file_backups (
    object_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id UUID NOT NULL REFERENCES agent_identities(id) ON DELETE CASCADE,
    backup_id UUID NOT NULL,
    file_id UUID NOT NULL,
    display_name TEXT NOT NULL,
    content_sha256 BYTEA NOT NULL CHECK (octet_length(content_sha256) = 32),
    byte_size BIGINT NOT NULL CHECK (byte_size > 0),
    status TEXT NOT NULL CHECK (status IN ('uploading', 'committed')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    committed_at TIMESTAMPTZ,
    UNIQUE (agent_id, backup_id, file_id),
    CHECK ((status = 'committed') = (committed_at IS NOT NULL))
);

CREATE INDEX android_file_backups_agent_created_idx
    ON android_file_backups (agent_id, created_at DESC);
