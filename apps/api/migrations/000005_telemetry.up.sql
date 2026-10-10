CREATE TABLE agent_telemetry (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id UUID NOT NULL REFERENCES agent_identities(id) ON DELETE CASCADE,
    report_id TEXT NOT NULL,
    observed_at TIMESTAMPTZ NOT NULL,
    payload JSONB NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (agent_id, report_id)
);

CREATE INDEX agent_telemetry_agent_observed_at_idx
    ON agent_telemetry (agent_id, observed_at DESC);
