DROP TABLE notification_settings;
UPDATE notification_deliveries SET status='delivered' WHERE status='skipped';
ALTER TABLE notification_deliveries DROP CONSTRAINT notification_deliveries_status_check;
ALTER TABLE notification_deliveries ADD CONSTRAINT notification_deliveries_status_check CHECK (status IN ('pending','sending','delivered','dead'));
ALTER TABLE notification_destinations DROP COLUMN notify_resolved;
ALTER TABLE notification_destinations DROP COLUMN notify_warning;
ALTER TABLE notification_destinations DROP COLUMN notify_critical;
