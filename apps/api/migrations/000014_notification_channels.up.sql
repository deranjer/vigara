-- Channels added in Settings > Notifications. Each is a delivery destination;
-- removing the destination removes the channel and its deliveries. sealed is
-- the channel's settings (URL, topic, token or webhook), encrypted with
-- VIGIRA_NOTIFICATION_MASTER_KEY and bound to the channel ID.
CREATE TABLE notification_channels (
 id text PRIMARY KEY REFERENCES notification_destinations(id) ON DELETE CASCADE,
 kind text NOT NULL CHECK (kind IN ('ntfy','gotify','discord')),
 name text NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
 sealed bytea NOT NULL CHECK (length(sealed) BETWEEN 16 AND 8192),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);

-- Environment channels already copied into notification_channels; their
-- variables are ignored from then on, even if the channel is later removed.
ALTER TABLE notification_settings ADD COLUMN env_imported text[] NOT NULL DEFAULT '{}';
