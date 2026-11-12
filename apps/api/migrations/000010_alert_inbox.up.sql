-- Alert inbox: incidents can be acknowledged (seen, still open until the
-- condition clears) or muted until a time, and carry free-text notes.
ALTER TABLE alert_incidents ADD COLUMN acknowledged_at timestamptz;
ALTER TABLE alert_incidents ADD COLUMN acknowledged_by uuid REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE alert_incidents ADD COLUMN muted_until timestamptz;
CREATE TABLE alert_notes (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 incident_id uuid NOT NULL REFERENCES alert_incidents(id) ON DELETE CASCADE,
 author_id uuid REFERENCES users(id) ON DELETE SET NULL,
 author_name text NOT NULL DEFAULT '',
 body text NOT NULL CHECK (length(body) BETWEEN 1 AND 2000),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX alert_notes_incident_idx ON alert_notes (incident_id, created_at);

-- Rules carry a severity. Losing a device or a backup is critical; the rest warn.
ALTER TABLE alert_rules ADD COLUMN severity text NOT NULL DEFAULT 'warning' CHECK (severity IN ('warning','critical'));
UPDATE alert_rules SET severity='critical' WHERE kind IN ('device_unavailable','backup_failure');

-- New-image-available notices are events, not alerts. Only an image that
-- changes under a fixed version tag raises an alert, under its own rule.
ALTER TABLE alert_rules DROP CONSTRAINT alert_rules_kind_check;
ALTER TABLE alert_rules ADD CONSTRAINT alert_rules_kind_check CHECK(kind IN ('device_unavailable','threshold','backup_failure','backup_overdue','image_update','pinned_image'));
UPDATE alert_rules SET name='Automatic image update failed', enabled=true, severity='critical' WHERE id='00000000-0000-0000-0000-000000000024';
INSERT INTO alert_rules(id,name,kind,enabled,severity,config) VALUES('00000000-0000-0000-0000-000000000025','Pinned image changed','pinned_image',true,'warning','{}');
UPDATE alert_incidents SET status='resolved', resolved_at=now(), updated_at=now()
 WHERE rule_id='00000000-0000-0000-0000-000000000024' AND dimension LIKE '%/image/%' AND status='firing';
