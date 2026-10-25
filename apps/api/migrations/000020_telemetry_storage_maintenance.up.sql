-- Retention and latest-snapshot updates create steady churn. Reclaim reusable
-- space and refresh planner statistics before the default 20%/10% thresholds.
ALTER TABLE agent_telemetry SET (autovacuum_vacuum_scale_factor = 0.02, autovacuum_analyze_scale_factor = 0.01);
ALTER TABLE agent_telemetry_latest SET (autovacuum_vacuum_scale_factor = 0.02, autovacuum_analyze_scale_factor = 0.01);
ALTER TABLE telemetry_metric_rollups SET (autovacuum_vacuum_scale_factor = 0.02, autovacuum_analyze_scale_factor = 0.01);
ALTER TABLE telemetry_report_dedup SET (autovacuum_vacuum_scale_factor = 0.02, autovacuum_analyze_scale_factor = 0.01);
