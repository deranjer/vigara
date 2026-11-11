package telemetry

import (
	"context"
	"fmt"
	"testing"
	"time"
)

type rollupTotals struct {
	count    int64
	sum      float64
	min, max float64
}

func TestHourlyRollupCountsEachSnapshotExactlyOnce(t *testing.T) {
	pool := telemetryTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var device, agent string
	if err := pool.QueryRow(ctx, `INSERT INTO devices(display_name) VALUES('rollup once') RETURNING id::text`).Scan(&device); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO agent_identities(device_id,credential_hash,protocol_version) VALUES($1,decode('ab12','hex'),'v1') RETURNING id::text`, device).Scan(&agent); err != nil {
		t.Fatal(err)
	}
	insert := func(report string, observed time.Time, value float64) {
		t.Helper()
		payload := fmt.Sprintf(`{"metrics":[{"name":"cpu_used_percent","unit":"%%","value":%v},{"name":"filesystem_free_bytes","unit":"By","value":%v,"labels":{"mount_path":"/"}}]}`, value, value*10)
		if _, err := pool.Exec(ctx, `INSERT INTO agent_telemetry(agent_id,report_id,observed_at,payload) VALUES($1,$2,$3,$4::jsonb)`, agent, report, observed, payload); err != nil {
			t.Fatal(err)
		}
	}
	totals := func(metric string, bucket time.Time) (rollupTotals, bool) {
		t.Helper()
		var r rollupTotals
		rows, err := pool.Query(ctx, `SELECT sample_count,sum_value,min_value,max_value FROM telemetry_metric_rollups WHERE agent_id=$1 AND metric_name=$2 AND bucket_start=$3`, agent, metric, bucket)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		if !rows.Next() {
			return r, false
		}
		if err = rows.Scan(&r.count, &r.sum, &r.min, &r.max); err != nil {
			t.Fatal(err)
		}
		return r, true
	}
	now := time.Now().UTC().Truncate(time.Hour).Add(30 * time.Minute)
	previous := now.Add(-time.Hour).Truncate(time.Hour)
	current := now.Truncate(time.Hour)
	insert("previous-1", previous.Add(5*time.Minute), 10)
	insert("previous-2", previous.Add(35*time.Minute), 30)
	insert("current-1", current.Add(10*time.Minute), 50)
	store := postgresStore{pool}

	for range 3 {
		if err := store.RollupCompletedHours(ctx, now); err != nil {
			t.Fatal(err)
		}
	}
	if got, ok := totals("cpu_used_percent", previous); !ok || got != (rollupTotals{2, 40, 10, 30}) {
		t.Fatalf("repeated passes must not add a snapshot twice: %+v found=%v", got, ok)
	}
	if got, ok := totals("filesystem_free_bytes", previous); !ok || got != (rollupTotals{2, 400, 100, 300}) {
		t.Fatalf("labelled series must be summarised too: %+v found=%v", got, ok)
	}
	if _, ok := totals("cpu_used_percent", current); ok {
		t.Fatal("the hour in progress must stay raw until it finishes")
	}
	var rolled, unrolled int
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE rolled_at IS NOT NULL), count(*) FILTER (WHERE rolled_at IS NULL) FROM agent_telemetry WHERE agent_id=$1`, agent).Scan(&rolled, &unrolled); err != nil || rolled != 2 || unrolled != 1 {
		t.Fatalf("rolled=%d unrolled=%d err=%v, want 2 and 1", rolled, unrolled, err)
	}

	// A delivery that arrives after its hour was summarised is added once.
	insert("previous-late", previous.Add(50*time.Minute), 60)
	for range 2 {
		if err := store.RollupCompletedHours(ctx, now); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := totals("cpu_used_percent", previous); got != (rollupTotals{3, 100, 10, 60}) {
		t.Fatalf("late delivery lost or counted twice: %+v", got)
	}

	// Retention later removes the raw rows. Rows already summarised must not be
	// summarised again, and the still-raw current-hour row must not be lost.
	cfg := RetentionConfig{RawAge: 25 * time.Hour, RollupAge: 90 * 24 * time.Hour}
	if err := store.Retain(ctx, cfg, now.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, _ := totals("cpu_used_percent", previous); got != (rollupTotals{3, 100, 10, 60}) {
		t.Fatalf("retention double counted summarised snapshots: %+v", got)
	}
	if got, ok := totals("cpu_used_percent", current); !ok || got != (rollupTotals{1, 50, 50, 50}) {
		t.Fatalf("retention must summarise a snapshot that was never rolled: %+v found=%v", got, ok)
	}
	var raw int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_telemetry WHERE agent_id=$1`, agent).Scan(&raw); err != nil || raw != 0 {
		t.Fatalf("expired raw rows remain: %d err=%v", raw, err)
	}
}

func TestHourlyRollupDrainsABacklogInBatches(t *testing.T) {
	pool := telemetryTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var device, agent string
	if err := pool.QueryRow(ctx, `INSERT INTO devices(display_name) VALUES('rollup backlog') RETURNING id::text`).Scan(&device); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO agent_identities(device_id,credential_hash,protocol_version) VALUES($1,decode('cd34','hex'),'v1') RETURNING id::text`, device).Scan(&agent); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Hour).Add(15 * time.Minute)
	// More than two batches, spread over several earlier hours, as after the upgrade.
	if _, err := pool.Exec(ctx, `INSERT INTO agent_telemetry(agent_id,report_id,observed_at,payload)
		SELECT $1,'b-'||g,$2::timestamptz-(g%5+1)*interval '1 hour'+(g%40)*interval '1 minute','{"metrics":[{"name":"cpu_used_percent","unit":"%","value":1}]}'::jsonb FROM generate_series(1,1200) g`, agent, now); err != nil {
		t.Fatal(err)
	}
	if err := (postgresStore{pool}).RollupCompletedHours(ctx, now); err != nil {
		t.Fatal(err)
	}
	var total, buckets int64
	if err := pool.QueryRow(ctx, `SELECT sum(sample_count), count(*) FROM telemetry_metric_rollups WHERE agent_id=$1`, agent).Scan(&total, &buckets); err != nil || total != 1200 || buckets != 5 {
		t.Fatalf("backlog summary total=%d buckets=%d err=%v, want 1200 and 5", total, buckets, err)
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_telemetry WHERE agent_id=$1 AND rolled_at IS NULL`, agent).Scan(&left); err != nil || left != 0 {
		t.Fatalf("%d snapshots left unrolled, err=%v", left, err)
	}
}
