CREATE TABLE alert_rules (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    kind TEXT NOT NULL CHECK (kind IN ('device_unavailable','threshold','backup_failure','backup_overdue')),
    enabled BOOLEAN NOT NULL DEFAULT true,
    config JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX alert_rules_enabled_idx ON alert_rules(enabled, kind);

CREATE TABLE alert_incidents (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    rule_id UUID NOT NULL REFERENCES alert_rules(id) ON DELETE CASCADE,
    dimension TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL CHECK (status IN ('firing','resolved')),
    summary TEXT NOT NULL,
    started_at TIMESTAMPTZ NOT NULL,
    last_observed_at TIMESTAMPTZ,
    resolved_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(rule_id, dimension)
);
CREATE INDEX alert_incidents_status_idx ON alert_incidents(status, updated_at DESC);

CREATE TABLE alert_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    incident_id UUID NOT NULL REFERENCES alert_incidents(id) ON DELETE CASCADE,
    transition TEXT NOT NULL CHECK (transition IN ('firing','resolved')),
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    payload JSONB NOT NULL,
    delivered_at TIMESTAMPTZ,
    UNIQUE(incident_id, transition, occurred_at)
);
CREATE INDEX alert_events_pending_idx ON alert_events(occurred_at) WHERE delivered_at IS NULL;

CREATE TABLE backup_status (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    device_id UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    policy_key TEXT NOT NULL CHECK (length(policy_key) BETWEEN 1 AND 128),
    status TEXT NOT NULL CHECK (status IN ('success','failure','running')),
    observed_at TIMESTAMPTZ NOT NULL,
    message TEXT NOT NULL DEFAULT '' CHECK (length(message) <= 512),
    source_id TEXT NOT NULL CHECK (length(source_id) BETWEEN 1 AND 128),
    UNIQUE(device_id, source_id)
);
CREATE INDEX backup_status_latest_idx ON backup_status(device_id, policy_key, observed_at DESC);
