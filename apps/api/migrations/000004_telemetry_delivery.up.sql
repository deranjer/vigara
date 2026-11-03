CREATE TABLE agent_telemetry_delivery (
 agent_id UUID PRIMARY KEY REFERENCES agent_identities(id) ON DELETE CASCADE,
 last_attempt_at TIMESTAMPTZ,
 last_status_code INTEGER,
 retry_at TIMESTAMPTZ,
 last_accepted_at TIMESTAMPTZ,
 queue_reported_at TIMESTAMPTZ,
 queued_snapshots INTEGER CHECK (queued_snapshots BETWEEN 0 AND 100),
 oldest_queued_at TIMESTAMPTZ,
 newest_queued_at TIMESTAMPTZ
);
