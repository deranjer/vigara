package telemetry

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"
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

// Retain folds a bounded set of old snapshots into UTC hourly aggregates and
// removes the corresponding raw rows in the same transaction.
func (s postgresStore) Retain(ctx context.Context, cfg RetentionConfig, now time.Time) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	for batch := 0; batch < 10; batch++ {
		done, err := s.retainBatch(ctx, cfg, now)
		if err != nil {
			return err
		}
		if done {
			break
		}
	}
	return nil
}

// retainBatch returns done when no raw samples remain eligible for rollup.
func (s postgresStore) retainBatch(ctx context.Context, cfg RetentionConfig, now time.Time) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var acquired bool
	if err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(140006)`).Scan(&acquired); err != nil {
		return false, err
	}
	if !acquired {
		return true, nil
	}
	rows, err := tx.Query(ctx, `SELECT id,agent_id::text,observed_at,payload FROM agent_telemetry WHERE observed_at < $1 ORDER BY observed_at LIMIT 500 FOR UPDATE SKIP LOCKED`, now.Add(-cfg.RawAge))
	if err != nil {
		return false, err
	}
	type rawRow struct {
		id, agent string
		observed  time.Time
		payload   []byte
	}
	var raw []rawRow
	for rows.Next() {
		var r rawRow
		if err = rows.Scan(&r.id, &r.agent, &r.observed, &r.payload); err != nil {
			rows.Close()
			return false, err
		}
		raw = append(raw, r)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return false, err
	}
	rows.Close()
	aggs := map[sampleKey]*sampleAggregate{}
	for _, row := range raw {
		var snapshot struct {
			Metrics []struct {
				Name   string            `json:"name"`
				Value  float64           `json:"value"`
				Labels map[string]string `json:"labels"`
			} `json:"metrics"`
		}
		if err = json.Unmarshal(row.payload, &snapshot); err != nil {
			return false, err
		}
		for _, metric := range snapshot.Metrics {
			if metric.Labels == nil {
				metric.Labels = map[string]string{}
			}
			labels, marshalErr := json.Marshal(metric.Labels)
			if marshalErr != nil {
				return false, marshalErr
			}
			bucket := row.observed.UTC().Truncate(time.Hour)
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
	rollups := make([]rollupRow, 0, len(aggs))
	for key, a := range aggs {
		rollups = append(rollups, rollupRow{key.agent, key.name, a.labels, key.bucket, a.count, a.sum, a.min, a.max})
	}
	for start := 0; start < len(rollups); start += 5000 {
		end := start + 5000
		if end > len(rollups) {
			end = len(rollups)
		}
		encoded, marshalErr := json.Marshal(rollups[start:end])
		if marshalErr != nil {
			return false, marshalErr
		}
		_, err = tx.Exec(ctx, `INSERT INTO telemetry_metric_rollups(agent_id,metric_name,labels,bucket_start,sample_count,sum_value,min_value,max_value)
			SELECT x.agent_id,x.metric_name,x.labels,x.bucket_start,x.sample_count,x.sum_value,x.min_value,x.max_value
			FROM jsonb_to_recordset($1::jsonb) AS x(agent_id uuid,metric_name text,labels jsonb,bucket_start timestamptz,sample_count bigint,sum_value double precision,min_value double precision,max_value double precision)
			ON CONFLICT(agent_id,metric_name,labels,bucket_start) DO UPDATE SET sample_count=telemetry_metric_rollups.sample_count+EXCLUDED.sample_count,sum_value=telemetry_metric_rollups.sum_value+EXCLUDED.sum_value,min_value=LEAST(telemetry_metric_rollups.min_value,EXCLUDED.min_value),max_value=GREATEST(telemetry_metric_rollups.max_value,EXCLUDED.max_value)`, encoded)
		if err != nil {
			return false, err
		}
	}
	if len(raw) > 0 {
		ids := make([]string, len(raw))
		for i, row := range raw {
			ids[i] = row.id
		}
		if _, err = tx.Exec(ctx, `DELETE FROM agent_telemetry WHERE id=ANY(string_to_array($1, ',')::uuid[])`, strings.Join(ids, ",")); err != nil {
			return false, err
		}
	}
	if _, err = tx.Exec(ctx, `DELETE FROM telemetry_metric_rollups WHERE ctid IN (SELECT ctid FROM telemetry_metric_rollups WHERE bucket_start < $1 LIMIT 10000)`, now.Add(-cfg.RollupAge)); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM telemetry_report_dedup WHERE ctid IN (SELECT ctid FROM telemetry_report_dedup WHERE received_at < $1 LIMIT 10000)`, now.Add(-(48*time.Hour + 5*time.Minute))); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return len(raw) == 0, nil
}

func RunRetentionWorker(ctx context.Context, store Store, cfg RetentionConfig, logger *slog.Logger) {
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	run := func() {
		workCtx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		if err := store.Retain(workCtx, cfg, time.Now().UTC()); err != nil {
			logger.Error("telemetry retention failed", "error", err)
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
