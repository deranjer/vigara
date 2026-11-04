package telemetry

import (
	"context"
	"log/slog"
	"time"
)

// LogStorageHealth reports bounded backlog counts and PostgreSQL's estimated
// live/dead rows. A count of 10001 means at least that many rows, not an exact
// backlog. Monitoring must not compete with cleanup by counting an entire table.
func (s postgresStore) LogStorageHealth(ctx context.Context, cfg RetentionConfig, logger *slog.Logger) {
	now := time.Now().UTC()
	var oldest *time.Time
	var raw, rollups, dedup int64
	err := s.pool.QueryRow(ctx, `SELECT
		(SELECT observed_at FROM agent_telemetry ORDER BY observed_at LIMIT 1),
		(SELECT count(*) FROM (SELECT 1 FROM agent_telemetry WHERE observed_at < $1 LIMIT 10001) t),
		(SELECT count(*) FROM (SELECT 1 FROM telemetry_metric_rollups WHERE bucket_start < $2 LIMIT 10001) t),
		(SELECT count(*) FROM (SELECT 1 FROM telemetry_report_dedup WHERE received_at < $3 LIMIT 10001) t)`,
		now.Add(-cfg.RawAge), now.Add(-cfg.RollupAge), now.Add(-(48*time.Hour+5*time.Minute))).Scan(&oldest, &raw, &rollups, &dedup)
	if err != nil {
		logger.Error("telemetry storage health unavailable", "error", err)
		return
	}
	lag := time.Duration(0)
	if oldest != nil && oldest.Before(now.Add(-cfg.RawAge)) {
		lag = now.Add(-cfg.RawAge).Sub(*oldest)
	}
	attrs := []any{"raw_backlog_capped", raw, "rollup_backlog_capped", rollups, "dedup_backlog_capped", dedup, "backlog_count_cap", 10001, "retention_lag_seconds", lag.Seconds()}
	if lag > 5*time.Minute || raw == 10001 || rollups == 10001 || dedup == 10001 {
		logger.Warn("telemetry retention backlog", attrs...)
	} else {
		logger.Info("telemetry retention healthy", attrs...)
	}
	rows, err := s.pool.Query(ctx, `SELECT relname, n_live_tup, n_dead_tup,
		pg_table_size(relid), pg_indexes_size(relid), last_autovacuum, last_autoanalyze
		FROM pg_stat_user_tables WHERE schemaname=current_schema() AND relname IN
		('agent_telemetry','agent_telemetry_latest','telemetry_metric_rollups','telemetry_report_dedup','telemetry_metric_series') ORDER BY relname`)
	if err != nil {
		logger.Error("telemetry table statistics unavailable", "error", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var live, dead, tableBytes, indexBytes int64
		var vacuum, analyze *time.Time
		if err := rows.Scan(&name, &live, &dead, &tableBytes, &indexBytes, &vacuum, &analyze); err != nil {
			logger.Error("telemetry table statistics unavailable", "error", err)
			return
		}
		logger.Info("telemetry table storage", "table", name, "estimated_live_rows", live, "estimated_dead_rows", dead,
			"table_bytes", tableBytes, "index_bytes", indexBytes, "last_autovacuum", vacuum, "last_autoanalyze", analyze)
	}
	if err := rows.Err(); err != nil {
		logger.Error("telemetry table statistics unavailable", "error", err)
	}
}
