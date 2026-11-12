-- Which alerts each channel receives. Channels themselves stay configured by
-- environment variables; these rows exist once a channel has been activated.
ALTER TABLE notification_destinations ADD COLUMN notify_critical boolean NOT NULL DEFAULT true;
ALTER TABLE notification_destinations ADD COLUMN notify_warning boolean NOT NULL DEFAULT true;
ALTER TABLE notification_destinations ADD COLUMN notify_resolved boolean NOT NULL DEFAULT true;

-- A delivery that was deliberately not sent: the alert was muted, or the
-- channel does not take that severity.
ALTER TABLE notification_deliveries DROP CONSTRAINT notification_deliveries_status_check;
ALTER TABLE notification_deliveries ADD CONSTRAINT notification_deliveries_status_check CHECK (status IN ('pending','sending','delivered','dead','skipped'));

-- Quiet hours hold everything except critical alerts until they end.
-- action_key signs the Acknowledge and Mute links sent inside notifications.
CREATE TABLE notification_settings (
 singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
 quiet_enabled boolean NOT NULL DEFAULT false,
 quiet_start time NOT NULL DEFAULT '23:00',
 quiet_end time NOT NULL DEFAULT '07:00',
 timezone text NOT NULL DEFAULT 'UTC' CHECK (length(timezone) BETWEEN 1 AND 128),
 action_key bytea NOT NULL DEFAULT public.gen_random_bytes(32),
 updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO notification_settings DEFAULT VALUES;
