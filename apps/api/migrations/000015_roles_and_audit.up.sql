ALTER TABLE users DROP CONSTRAINT users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check CHECK (role IN ('viewer', 'operator', 'admin'));

CREATE TABLE audit_events (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    actor_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    actor_name TEXT NOT NULL CHECK (length(actor_name) BETWEEN 1 AND 128),
    action TEXT NOT NULL CHECK (length(action) BETWEEN 1 AND 64),
    target_kind TEXT NOT NULL DEFAULT '' CHECK (length(target_kind) <= 64),
    target_id TEXT NOT NULL DEFAULT '' CHECK (length(target_id) <= 256),
    agent_id UUID REFERENCES agent_identities(id) ON DELETE SET NULL,
    outcome TEXT NOT NULL CHECK (outcome IN ('success', 'failure', 'denied')),
    detail JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX audit_events_occurred_idx ON audit_events (occurred_at DESC, id DESC);
CREATE INDEX audit_events_agent_idx ON audit_events (agent_id, occurred_at DESC) WHERE agent_id IS NOT NULL;
CREATE INDEX audit_events_actor_idx ON audit_events (actor_user_id, occurred_at DESC) WHERE actor_user_id IS NOT NULL;
