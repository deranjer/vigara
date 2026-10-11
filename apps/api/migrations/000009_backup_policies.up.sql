CREATE TABLE backup_policies (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    device_id UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    agent_id UUID NOT NULL REFERENCES agent_identities(id) ON DELETE CASCADE,
    profile_id TEXT NOT NULL CHECK (length(profile_id) BETWEEN 1 AND 64),
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    destination TEXT NOT NULL CHECK (length(destination) BETWEEN 1 AND 2048),
    included_paths JSONB NOT NULL CHECK (jsonb_typeof(included_paths) = 'array'),
    excluded_paths JSONB NOT NULL CHECK (jsonb_typeof(excluded_paths) = 'array'),
    retention JSONB NOT NULL CHECK (jsonb_typeof(retention) = 'object'),
    schedule JSONB NOT NULL CHECK (jsonb_typeof(schedule) = 'object'),
    missed_run_policy TEXT NOT NULL CHECK (missed_run_policy IN ('skip', 'run_once')),
    disconnected_device_policy TEXT NOT NULL CHECK (disconnected_device_policy IN ('wait', 'skip')),
    enabled BOOLEAN NOT NULL DEFAULT true,
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    retired_at TIMESTAMPTZ,
    next_run_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (profile_id ~ '^[a-z0-9_-]+$')
);
CREATE UNIQUE INDEX backup_policies_agent_profile_active_idx ON backup_policies(agent_id, profile_id) WHERE retired_at IS NULL;
CREATE INDEX backup_policies_due_idx ON backup_policies(next_run_at) WHERE enabled;

CREATE TABLE backup_jobs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    policy_id UUID NOT NULL REFERENCES backup_policies(id) ON DELETE RESTRICT,
    agent_id UUID NOT NULL REFERENCES agent_identities(id) ON DELETE RESTRICT,
    profile_id TEXT NOT NULL CHECK (length(profile_id) BETWEEN 1 AND 64),
    policy_revision INTEGER NOT NULL CHECK (policy_revision > 0),
    policy_snapshot JSONB NOT NULL CHECK (jsonb_typeof(policy_snapshot) = 'object'),
    scheduled_for TIMESTAMPTZ NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('waiting', 'pending', 'leased', 'completed', 'skipped')) DEFAULT 'pending',
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count BETWEEN 0 AND 100),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(policy_id, scheduled_for)
);
CREATE INDEX backup_jobs_agent_pending_idx ON backup_jobs(agent_id, created_at) WHERE status = 'pending';

CREATE TABLE backup_job_attempts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id UUID NOT NULL REFERENCES backup_jobs(id) ON DELETE RESTRICT,
    attempt_number INTEGER NOT NULL CHECK (attempt_number BETWEEN 1 AND 100),
    lease_token UUID NOT NULL UNIQUE DEFAULT gen_random_uuid(),
    leased_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_expires_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ,
    result_status TEXT CHECK (result_status IN ('succeeded', 'failed', 'timed_out', 'cancelled')),
    UNIQUE(job_id, attempt_number),
    CHECK (lease_expires_at > leased_at)
);
CREATE INDEX backup_job_attempts_active_idx ON backup_job_attempts(lease_expires_at) WHERE finished_at IS NULL;
