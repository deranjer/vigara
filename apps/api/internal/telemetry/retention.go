package telemetry

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type sampleKey struct {
	agent  string
	name   string
	labels string
	bucket time.Time
}
type sampleAggregate struct {
	labels        json.RawMessage
	count         int64
	sum, min, max float64
}

// rollupBatchSize bounds one transaction of the continuous hourly summary pass.
const rollupBatchSize = 500

// rawSnapshot is the part of agent_telemetry needed to build hourly summaries.
type rawSnapshot struct {
	id, agent string
	observed  time.Time
	payload   []byte
}

type rollupRow struct {
	AgentID    string          `json:"agent_id"`
	MetricName string          `json:"metric_name"`
	Labels     json.RawMessage `json:"labels"`
	Bucket     time.Time       `json:"bucket_start"`
	Count      int64           `json:"sample_count"`
	Sum        float64         `json:"sum_value"`
	Min        float64         `json:"min_value"`
	Max        float64         `json:"max_value"`
}

func scanSnapshots(rows pgx.Rows) ([]rawSnapshot, error) {
	defer rows.Close()
	var raw []rawSnapshot
	for rows.Next() {
		var r rawSnapshot
		if err := rows.Scan(&r.id, &r.agent, &r.observed, &r.payload); err != nil {
			return nil, err
		}
		raw = append(raw, r)
	}
	return raw, rows.Err()
}

// aggregateSnapshots folds snapshots into per-agent, per-series hourly counts,
// sums and extrema. Combining hours later uses sum/count, never averages of averages.
func aggregateSnapshots(raw []rawSnapshot) ([]rollupRow, error) {
	aggs := map[sampleKey]*sampleAggregate{}
	for _, row := range raw {
		var snapshot struct {
			Metrics []struct {
				Name   string            `json:"name"`
				Value  float64           `json:"value"`
				Labels map[string]string `json:"labels"`
			} `json:"metrics"`
		}
		if err := json.Unmarshal(row.payload, &snapshot); err != nil {
			return nil, err
		}
		bucket := row.observed.UTC().Truncate(time.Hour)
		for _, metric := range snapshot.Metrics {
			if metric.Labels == nil {
				metric.Labels = map[string]string{}
			}
			labels, err := json.Marshal(metric.Labels)
			if err != nil {
				return nil, err
			}
			key := sampleKey{row.agent, metric.Name, string(labels), bucket}
			a := aggs[key]
			if a == nil {
				a = &sampleAggregate{labels: labels, min: metric.Value, max: metric.Value}
				aggs[key] = a
			}
			a.count++
			a.sum += metric.Value
			if metric.Value < a.min {
				a.min = metric.Value
			}
			if metric.Value > a.max {
				a.max = metric.Value
			}
		}
	}
	rollups := make([]rollupRow, 0, len(aggs))
	for key, a := range aggs {
		rollups = append(rollups, rollupRow{key.agent, key.name, a.labels, key.bucket, a.count, a.sum, a.min, a.max})
	}
	return rollups, nil
}

// addRollups merges hourly aggregates into telemetry_metric_rollups by adding
// to any existing bucket. Callers must guarantee each snapshot is added once.
func addRollups(ctx context.Context, tx pgx.Tx, rollups []rollupRow) error {
	for start := 0; start < len(rollups); start += 5000 {
		end := min(start+5000, len(rollups))
		encoded, err := json.Marshal(rollups[start:end])
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO telemetry_metric_rollups(agent_id,metric_name,labels,bucket_start,sample_count,sum_value,min_value,max_value)
			SELECT x.agent_id,x.metric_name,x.labels,x.bucket_start,x.sample_count,x.sum_value,x.min_value,x.max_value
			FROM jsonb_to_recordset($1::jsonb) AS x(agent_id uuid,metric_name text,labels jsonb,bucket_start timestamptz,sample_count bigint,sum_value double precision,min_value double precision,max_value double precision)
			ON CONFLICT(agent_id,metric_name,labels,bucket_start) DO UPDATE SET sample_count=telemetry_metric_rollups.sample_count+EXCLUDED.sample_count,sum_value=telemetry_metric_rollups.sum_value+EXCLUDED.sum_value,min_value=LEAST(telemetry_metric_rollups.min_value,EXCLUDED.min_value),max_value=GREATEST(telemetry_metric_rollups.max_value,EXCLUDED.max_value)`, encoded); err != nil {
			return err
		}
	}
	return nil
}

func snapshotIDs(raw []rawSnapshot) string {
	ids := make([]string, len(raw))
	for i, row := range raw {
		ids[i] = row.id
	}
	return strings.Join(ids, ",")
}

// RollupCompletedHours adds snapshots from finished UTC hours to the hourly
// summaries, so ranges longer than a day never need to scan raw JSON. The
// current hour stays raw because it is still changing. Snapshots delivered late
// for an earlier hour are still unrolled and are added on the next pass. Each
// snapshot is marked rolled_at in the same transaction that adds it, so it is
// counted exactly once and queries can take "rolled summaries plus unrolled raw
// rows" without double counting.
func (s postgresStore) RollupCompletedHours(ctx context.Context, now time.Time) error {
	boundary := now.UTC().Truncate(time.Hour)
	for {
		done, err := s.rollupBatch(ctx, boundary, now)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
	}
}

func (s postgresStore) rollupBatch(ctx context.Context, boundary, now time.Time) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	// Shares retention's lock so neither pass can delete or re-add rows the other holds.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(140006)`); err != nil {
		return false, err
	}
	rows, err := tx.Query(ctx, `SELECT id,agent_id::text,observed_at,payload FROM agent_telemetry WHERE rolled_at IS NULL AND observed_at < $1 ORDER BY observed_at LIMIT $2 FOR UPDATE SKIP LOCKED`, boundary, rollupBatchSize)
	if err != nil {
		return false, err
	}
	raw, err := scanSnapshots(rows)
	if err != nil {
		return false, err
	}
	if len(raw) == 0 {
		return true, nil
	}
	rollups, err := aggregateSnapshots(raw)
	if err != nil {
		return false, err
	}
	if err = addRollups(ctx, tx, rollups); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE agent_telemetry SET rolled_at=$2 WHERE id=ANY(string_to_array($1, ',')::uuid[])`, snapshotIDs(raw), now); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return len(raw) < rollupBatchSize, nil
}

// Retain drains expired history in small transactions until it is caught up or
// ctx expires. Each transaction aggregates and removes its raw rows atomically.
func (s postgresStore) Retain(ctx context.Context, cfg RetentionConfig, now time.Time) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	// Each transaction stays small, but a pass drains all eligible work until
	// its context expires. A fixed batch cap can permanently fall behind intake.
	for {
		done, err := s.retainBatch(ctx, cfg, now)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
	}
}

// retainBatch returns done when raw and auxiliary cleanup are both caught up.
func (s postgresStore) retainBatch(ctx context.Context, cfg RetentionConfig, now time.Time) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	// Waiting is bounded by the pass context. Lock contention is not evidence
	// that cleanup completed, and must not masquerade as an empty backlog.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(140006)`); err != nil {
		return false, err
	}
	rows, err := tx.Query(ctx, `SELECT id,agent_id::text,observed_at,payload FROM agent_telemetry WHERE observed_at < $1 ORDER BY observed_at LIMIT 500 FOR UPDATE SKIP LOCKED`, now.Add(-cfg.RawAge))
	if err != nil {
		return false, err
	}
	raw, err := scanSnapshots(rows)
	if err != nil {
		return false, err
	}
	if len(raw) > 0 {
		// Only snapshots the hourly pass has not added yet still need summarising;
		// adding the others again would count them twice.
		unrolledRows, queryErr := tx.Query(ctx, `SELECT id,agent_id::text,observed_at,payload FROM agent_telemetry WHERE id=ANY(string_to_array($1, ',')::uuid[]) AND rolled_at IS NULL`, snapshotIDs(raw))
		if queryErr != nil {
			return false, queryErr
		}
		unrolled, scanErr := scanSnapshots(unrolledRows)
		if scanErr != nil {
			return false, scanErr
		}
		rollups, aggErr := aggregateSnapshots(unrolled)
		if aggErr != nil {
			return false, aggErr
		}
		if err = addRollups(ctx, tx, rollups); err != nil {
			return false, err
		}
		if _, err = tx.Exec(ctx, `DELETE FROM agent_telemetry WHERE id=ANY(string_to_array($1, ',')::uuid[])`, snapshotIDs(raw)); err != nil {
			return false, err
		}
	}
	rollupDelete, err := tx.Exec(ctx, `DELETE FROM telemetry_metric_rollups WHERE ctid IN (SELECT ctid FROM telemetry_metric_rollups WHERE bucket_start < $1 LIMIT 10000)`, now.Add(-cfg.RollupAge))
	if err != nil {
		return false, err
	}
	dedupDelete, err := tx.Exec(ctx, `DELETE FROM telemetry_report_dedup WHERE ctid IN (SELECT ctid FROM telemetry_report_dedup WHERE received_at < $1 LIMIT 10000)`, now.Add(-(48*time.Hour + 5*time.Minute)))
	if err != nil {
		return false, err
	}
	// Registry entries describe retained history, not an agent's lifetime quota.
	seriesDelete, err := tx.Exec(ctx, `DELETE FROM telemetry_metric_series WHERE ctid IN (SELECT ctid FROM telemetry_metric_series WHERE last_observed_at < $1 ORDER BY last_observed_at LIMIT 10000 FOR UPDATE)`, now.Add(-cfg.RollupAge))
	if err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	// Expired rollups and dedup keys must drain even when no raw rows remain.
	return len(raw) == 0 && rollupDelete.RowsAffected() < 10000 && dedupDelete.RowsAffected() < 10000 && seriesDelete.RowsAffected() < 10000, nil
}

// hourlyRoller is implemented by stores that keep hourly summaries current.
type hourlyRoller interface {
	RollupCompletedHours(context.Context, time.Time) error
}

// rollupPass runs the hourly summary pass with its own deadline so a first-run
// backlog cannot starve retention in the same cycle.
func rollupPass(ctx context.Context, store Store, now time.Time, logger *slog.Logger) {
	roller, ok := store.(hourlyRoller)
	if !ok {
		return
	}
	rollCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := roller.RollupCompletedHours(rollCtx, now); err != nil {
		logger.Error("telemetry hourly rollup failed", "error", err)
	}
}

func RunRetentionWorker(ctx context.Context, store Store, cfg RetentionConfig, logger *slog.Logger) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	run := func() {
		workCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		started := time.Now()
		rollupPass(workCtx, store, started.UTC(), logger)
		if err := store.Retain(workCtx, cfg, started.UTC()); err != nil {
			logger.Error("telemetry retention failed", "error", err)
		}
		if reporter, ok := store.(interface {
			LogStorageHealth(context.Context, RetentionConfig, *slog.Logger)
		}); ok && ctx.Err() == nil {
			healthCtx, healthCancel := context.WithTimeout(ctx, 5*time.Second)
			reporter.LogStorageHealth(healthCtx, cfg, logger.With("retention_duration_ms", time.Since(started).Milliseconds()))
			healthCancel()
		}
	}
	run()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func RetentionPass(ctx context.Context, store Store, cfg RetentionConfig, logger *slog.Logger) error {
	work, cancel := context.WithTimeout(ctx, 45*time.Second)
	started := time.Now()
	rollupPass(work, store, started.UTC(), logger)
	err := store.Retain(work, cfg, started.UTC())
	cancel()
	if reporter, ok := store.(interface {
		LogStorageHealth(context.Context, RetentionConfig, *slog.Logger)
	}); ok && ctx.Err() == nil {
		health, cancelHealth := context.WithTimeout(ctx, 5*time.Second)
		reporter.LogStorageHealth(health, cfg, logger.With("retention_duration_ms", time.Since(started).Milliseconds()))
		cancelHealth()
	}
	return err
}
