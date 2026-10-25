package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestRetentionDrainsBeyondOldCapAndExpiresAuxiliaryRows(t *testing.T) {
	pool := telemetryTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var device, agent string
	if err := pool.QueryRow(ctx, `INSERT INTO devices(display_name) VALUES('retention backlog') RETURNING id::text`).Scan(&device); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO agent_identities(device_id,credential_hash,protocol_version) VALUES($1,decode('ccdd','hex'),'v1') RETURNING id::text`, device).Scan(&agent); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Hour)
	old := now.Add(-31 * 24 * time.Hour)
	payload, _ := json.Marshal(validSnapshot(agent, "backlog", old))
	if _, err := pool.Exec(ctx, `INSERT INTO agent_telemetry(agent_id,report_id,observed_at,payload)
		SELECT $1,'backlog-'||g,$2,$3::jsonb FROM generate_series(1,6001) g`, agent, old, payload); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agent_telemetry(agent_id,report_id,observed_at,payload)
		VALUES($1,'month-boundary',$2,$3::jsonb),($1,'recent',$2+interval '1 hour',$3::jsonb)`, agent, now.Add(-30*24*time.Hour), payload); err != nil {
		t.Fatal(err)
	}
	// Auxiliary cleanup must continue across multiple batches without raw work.
	if _, err := pool.Exec(ctx, `INSERT INTO telemetry_report_dedup(agent_id,report_id,received_at)
		SELECT $1,'expired-'||g,$2 FROM generate_series(1,20001) g`, agent, old); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO telemetry_metric_rollups(agent_id,metric_name,bucket_start,sample_count,sum_value,min_value,max_value)
		SELECT $1,'expired', $2::timestamptz-g*interval '1 hour',1,1,1,1 FROM generate_series(1,20001) g`, agent, now.Add(-365*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	cfg := RetentionConfig{RawAge: 30 * 24 * time.Hour, RollupAge: 365 * 24 * time.Hour}
	store := postgresStore{pool}
	if err := store.Retain(ctx, cfg, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO telemetry_metric_rollups(agent_id,metric_name,bucket_start,sample_count,sum_value,min_value,max_value)
		VALUES($1,'year-boundary',$2,1,1,1,1)`, agent, now.Add(-365*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// With no eligible raw rows, auxiliary tables must still drain fully.
	if _, err := pool.Exec(ctx, `INSERT INTO telemetry_report_dedup(agent_id,report_id,received_at)
		SELECT $1,'auxiliary-'||g,$2 FROM generate_series(1,20001) g`, agent, old); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO telemetry_metric_rollups(agent_id,metric_name,bucket_start,sample_count,sum_value,min_value,max_value)
		SELECT $1,'expired', $2::timestamptz-g*interval '1 hour',1,1,1,1 FROM generate_series(1,20001) g`, agent, now.Add(-365*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.Retain(ctx, cfg, now); err != nil {
		t.Fatal(err)
	}
	var remaining, expired, dedup int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM agent_telemetry WHERE agent_id=$1),
		(SELECT count(*) FROM telemetry_metric_rollups WHERE agent_id=$1 AND metric_name='expired'),
		(SELECT count(*) FROM telemetry_report_dedup WHERE agent_id=$1)`, agent).Scan(&remaining, &expired, &dedup); err != nil {
		t.Fatal(err)
	}
	if remaining != 2 || expired != 0 || dedup != 0 {
		t.Fatalf("raw=%d expired rollups=%d dedup=%d", remaining, expired, dedup)
	}
	var count int64
	if err := pool.QueryRow(ctx, `SELECT sample_count FROM telemetry_metric_rollups WHERE agent_id=$1 AND bucket_start=$2`, agent, old).Scan(&count); err != nil || count != 6001 {
		t.Fatalf("aggregated count=%d err=%v", count, err)
	}
	if err := store.Retain(ctx, cfg, now); err != nil {
		t.Fatal(err)
	}
	var repeated int64
	if err := pool.QueryRow(ctx, `SELECT sample_count FROM telemetry_metric_rollups WHERE agent_id=$1 AND bucket_start=$2`, agent, old).Scan(&repeated); err != nil || repeated != count {
		t.Fatalf("repeat count=%d err=%v", repeated, err)
	}
	// Fresh health sampling uses wall time, so the exact month boundary can now
	// be eligible; the important contract is a structured, available report.
	var logs bytes.Buffer
	store.LogStorageHealth(ctx, cfg, slog.New(slog.NewJSONHandler(&logs, nil)))
	if strings.Contains(logs.String(), "unavailable") || !strings.Contains(logs.String(), "retention_lag_seconds") || !strings.Contains(logs.String(), "estimated_dead_rows") {
		t.Fatalf("missing health report: %s", logs.String())
	}
	var boundary int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM telemetry_metric_rollups WHERE agent_id=$1 AND metric_name='year-boundary'`, agent).Scan(&boundary); err != nil || boundary != 1 {
		t.Fatalf("exact year boundary should survive: count=%d err=%v", boundary, err)
	}
	if err := store.Retain(ctx, cfg, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM telemetry_metric_rollups WHERE agent_id=$1 AND metric_name='year-boundary'`, agent).Scan(&boundary); err != nil || boundary != 0 {
		t.Fatalf("data older than one year survived: count=%d err=%v", boundary, err)
	}
}
