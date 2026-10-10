DROP TABLE IF EXISTS agent_reports;
DROP TABLE IF EXISTS agent_enrollment_tokens;
ALTER TABLE agent_identities
    DROP COLUMN IF EXISTS capabilities,
    DROP COLUMN IF EXISTS allowed_capabilities,
    DROP COLUMN IF EXISTS agent_version,
    DROP COLUMN IF EXISTS architecture,
    DROP COLUMN IF EXISTS platform;
