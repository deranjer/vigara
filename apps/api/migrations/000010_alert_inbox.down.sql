DELETE FROM alert_rules WHERE id='00000000-0000-0000-0000-000000000025';
UPDATE alert_rules SET name='Container image updates', enabled=false WHERE id='00000000-0000-0000-0000-000000000024';
ALTER TABLE alert_rules DROP CONSTRAINT alert_rules_kind_check;
ALTER TABLE alert_rules ADD CONSTRAINT alert_rules_kind_check CHECK(kind IN ('device_unavailable','threshold','backup_failure','backup_overdue','image_update'));
ALTER TABLE alert_rules DROP COLUMN severity;
DROP TABLE alert_notes;
ALTER TABLE alert_incidents DROP COLUMN muted_until;
ALTER TABLE alert_incidents DROP COLUMN acknowledged_by;
ALTER TABLE alert_incidents DROP COLUMN acknowledged_at;
