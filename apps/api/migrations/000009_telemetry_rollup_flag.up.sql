-- Hourly summaries are now built continuously from completed hours instead of
-- only when raw snapshots expire. rolled_at records that a snapshot has been
-- added to telemetry_metric_rollups, so each snapshot is counted exactly once
-- even when late deliveries arrive for an hour that was already summarised.
-- Existing rows stay NULL and are summarised by the worker on its next pass.
ALTER TABLE agent_telemetry ADD COLUMN rolled_at timestamptz;
-- Only the current hour and any backlog or late deliveries stay unrolled, so
-- this index stays tiny while serving both the worker and history queries.
CREATE INDEX agent_telemetry_unrolled_idx ON agent_telemetry (observed_at, agent_id) WHERE rolled_at IS NULL;
