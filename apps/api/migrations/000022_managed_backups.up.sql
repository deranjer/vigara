ALTER TABLE agent_enrollment_tokens ADD COLUMN managed_backup BYTEA;
CREATE TABLE managed_backup_profiles (
    agent_id UUID PRIMARY KEY REFERENCES agent_identities(id) ON DELETE CASCADE,
    policy_id UUID NOT NULL UNIQUE REFERENCES backup_policies(id) ON DELETE CASCADE,
    config BYTEA NOT NULL,
    revision BIGINT NOT NULL DEFAULT 1,
    applied_revision BIGINT NOT NULL DEFAULT 0,
    initialized BOOLEAN NOT NULL DEFAULT false,
    error_code TEXT NOT NULL DEFAULT '',
    reported_at TIMESTAMPTZ
);
