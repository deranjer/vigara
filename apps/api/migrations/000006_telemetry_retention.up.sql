CREATE TABLE telemetry_ingest_limits (
    agent_id UUID PRIMARY KEY REFERENCES agent_identities(id) ON DELETE CASCADE,
    window_started_at TIMESTAMPTZ NOT NULL,
    snapshot_count INTEGER NOT NULL DEFAULT 0 CHECK (snapshot_count >= 0)
);

CREATE TABLE telemetry_report_dedup (
    agent_id UUID NOT NULL REFERENCES agent_identities(id) ON DELETE CASCADE,
    report_id TEXT NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (agent_id, report_id)
);
CREATE INDEX telemetry_report_dedup_received_idx ON telemetry_report_dedup(received_at);

CREATE TABLE telemetry_metric_series (
    agent_id UUID NOT NULL REFERENCES agent_identities(id) ON DELETE CASCADE,
    metric_name TEXT NOT NULL,
    labels JSONB NOT NULL DEFAULT '{}'::jsonb,
    legacy BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (agent_id, metric_name, labels)
);

CREATE TABLE telemetry_metric_rollups (
    agent_id UUID NOT NULL REFERENCES agent_identities(id) ON DELETE CASCADE,
    metric_name TEXT NOT NULL,
    labels JSONB NOT NULL DEFAULT '{}'::jsonb,
    bucket_start TIMESTAMPTZ NOT NULL,
    sample_count BIGINT NOT NULL CHECK (sample_count > 0),
    sum_value DOUBLE PRECISION NOT NULL,
    min_value DOUBLE PRECISION NOT NULL,
    max_value DOUBLE PRECISION NOT NULL,
    PRIMARY KEY (agent_id, metric_name, labels, bucket_start)
);
CREATE INDEX telemetry_metric_rollups_bucket_idx ON telemetry_metric_rollups(bucket_start);

CREATE TABLE agent_telemetry_latest (
    agent_id UUID PRIMARY KEY REFERENCES agent_identities(id) ON DELETE CASCADE,
    report_id TEXT NOT NULL,
    observed_at TIMESTAMPTZ NOT NULL,
    payload JSONB NOT NULL,
    received_at TIMESTAMPTZ NOT NULL
);

-- Preserve deduplication for rows created before retention was introduced.
INSERT INTO telemetry_report_dedup(agent_id,report_id,received_at)
SELECT agent_id,report_id,received_at FROM agent_telemetry
ON CONFLICT DO NOTHING;

-- Seed the bounded series registry from retained historical payloads.
INSERT INTO telemetry_metric_series(agent_id,metric_name,labels,legacy)
SELECT DISTINCT t.agent_id, metric->>'name', COALESCE(NULLIF(metric->'labels','null'::jsonb),'{}'::jsonb), true
FROM agent_telemetry t
CROSS JOIN LATERAL jsonb_array_elements(COALESCE(t.payload->'metrics','[]'::jsonb)) AS metric(metric)
ON CONFLICT DO NOTHING;

-- Keep the newest host report available even after its raw row is rolled up.
INSERT INTO agent_telemetry_latest(agent_id,report_id,observed_at,payload,received_at)
SELECT DISTINCT ON (agent_id) agent_id,report_id,observed_at,payload,received_at
FROM agent_telemetry
ORDER BY agent_id,observed_at DESC,received_at DESC
ON CONFLICT(agent_id) DO NOTHING;

CREATE INDEX agent_telemetry_observed_at_idx ON agent_telemetry(observed_at,id);
