DROP INDEX IF EXISTS agent_telemetry_unrolled_idx;
ALTER TABLE agent_telemetry DROP COLUMN IF EXISTS rolled_at;
