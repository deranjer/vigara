-- Destination activation is durable: re-enabling the same destination ID must
-- resume its existing cursor and pending acknowledgments.
CREATE TABLE notification_destinations (
    id TEXT PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 120),
    activated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE notification_deliveries (
    event_id UUID NOT NULL REFERENCES alert_events(id) ON DELETE CASCADE,
    destination_id TEXT NOT NULL REFERENCES notification_destinations(id) ON DELETE CASCADE,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','sending','delivered','dead')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_token UUID,
    lease_until TIMESTAMPTZ,
    last_error_code TEXT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (event_id, destination_id),
    CHECK ((status = 'sending' AND lease_token IS NOT NULL AND lease_until IS NOT NULL)
        OR (status <> 'sending' AND lease_token IS NULL AND lease_until IS NULL))
);

CREATE INDEX notification_deliveries_claim_idx
    ON notification_deliveries(destination_id, next_attempt_at, updated_at)
    WHERE status IN ('pending','sending');

CREATE INDEX notification_seed_events_idx ON alert_events(occurred_at,id);

CREATE INDEX notification_deliveries_incident_order_idx
    ON notification_deliveries(destination_id, event_id, status);
