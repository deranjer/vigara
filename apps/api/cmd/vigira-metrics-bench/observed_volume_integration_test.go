package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// This opt-in integration test creates isolated schemas in a local test/dev/
// bench database. It never runs against a network or production database.
func TestObservedVolumePostgresWindowsAndFailureStates(t *testing.T) {
	dsn := os.Getenv("VIGIRA_METRICS_BENCH_TEST_DSN")
	if dsn == "" {
		t.Skip("set VIGIRA_METRICS_BENCH_TEST_DSN to a local test/dev/bench PostgreSQL database")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if !localTestTarget(cfg.Host, cfg.Database) {
		t.Fatalf("integration test requires loopback PostgreSQL and a test/dev/bench database, got host=%q database=%q", cfg.Host, cfg.Database)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := openPool(ctx, dsn, "")
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		t.Fatal(err)
	}
	schema := "vigira_observed_volume_" + hex.EncodeToString(bytes[:])
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanCancel()
		if _, err := admin.Exec(cleanCtx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	}()
	if _, err := admin.Exec(ctx, `CREATE TABLE `+schema+`.agent_telemetry (
		id uuid PRIMARY KEY, agent_id uuid NOT NULL, observed_at timestamptz NOT NULL, payload jsonb NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE INDEX ON "+schema+".agent_telemetry(observed_at,id)"); err != nil {
		t.Fatal(err)
	}
	pool, err := openTimedPool(ctx, dsn, schema, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	anchor := time.Date(2026, 10, 11, 16, 0, 0, 0, time.UTC)
	seed := []struct {
		id, agent string
		observed  time.Time
		metrics   int
	}{
		{"50000000-0000-0000-0000-000000000001", "60000000-0000-0000-0000-000000000001", anchor.Add(-24 * time.Hour), 9}, // included lower bound
		{"50000000-0000-0000-0000-000000000002", "60000000-0000-0000-0000-000000000001", anchor.Add(-24*time.Hour + time.Microsecond), 1},
		{"50000000-0000-0000-0000-000000000003", "60000000-0000-0000-0000-000000000002", anchor.Add(-time.Hour), 2}, // included recent-hour lower bound
		{"50000000-0000-0000-0000-000000000004", "60000000-0000-0000-0000-000000000003", anchor.Add(-time.Microsecond), 1},
		{"50000000-0000-0000-0000-000000000005", "60000000-0000-0000-0000-000000000004", anchor, 9},                  // excluded upper bound
		{"50000000-0000-0000-0000-000000000006", "60000000-0000-0000-0000-000000000005", anchor.Add(time.Minute), 9}, // future excluded
	}
	for _, row := range seed {
		payload, err := json.Marshal(map[string]any{"metrics": make([]int, row.metrics)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := admin.Exec(ctx, "INSERT INTO "+schema+`.agent_telemetry(id,agent_id,observed_at,payload) VALUES($1,$2,$3,$4::jsonb)`, row.id, row.agent, row.observed, payload); err != nil {
			t.Fatal(err)
		}
	}

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer func(held pgx.Tx) { _ = held.Rollback(context.Background()) }(tx)
	volume := observedVolumeReport(ctx, tx, anchor)
	if volume.Status != "available" || volume.RecentHour.SnapshotRows != 2 || volume.RecentHour.MetricDatapoints != 3 || volume.RecentHour.DistinctReportingAgents != 2 {
		t.Fatalf("recent hour counts = %+v", volume.RecentHour)
	}
	if volume.Recent24Hours.SnapshotRows != 4 || volume.Recent24Hours.MetricDatapoints != 13 || volume.Recent24Hours.DistinctReportingAgents != 3 {
		t.Fatalf("24-hour counts = %+v", volume.Recent24Hours)
	}
	if len(volume.HourlyBuckets) != 24 || volume.HourlyBuckets[0].SnapshotRows != 2 || volume.HourlyBuckets[23].SnapshotRows != 2 || volume.HourlyBuckets[22].Status != "empty" {
		t.Fatalf("hourly buckets did not honor [start,end) bounds: first=%+v recent=%+v", volume.HourlyBuckets[0], volume.HourlyBuckets[23])
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := admin.Exec(ctx, "DELETE FROM "+schema+".agent_telemetry"); err != nil {
		t.Fatal(err)
	}
	tx, err = pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer func(held pgx.Tx) { _ = held.Rollback(context.Background()) }(tx)
	empty := observedVolumeReport(ctx, tx, anchor)
	if empty.Status != "empty" || empty.RecentHour.Status != "empty" || len(empty.HourlyBuckets) != 24 {
		t.Fatalf("empty table classification = %+v", empty)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema+"_missing"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanCancel()
		if _, err := admin.Exec(cleanCtx, "DROP SCHEMA "+schema+"_missing CASCADE"); err != nil {
			t.Errorf("drop missing-table test schema: %v", err)
		}
	}()
	missingPool, err := openTimedPool(ctx, dsn, schema+"_missing", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer missingPool.Close()
	tx, err = missingPool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer func(held pgx.Tx) { _ = held.Rollback(context.Background()) }(tx)
	missing := observedVolumeReport(ctx, tx, anchor)
	if missing.Status != "unavailable" || missing.UnavailableReason == "" || missing.RecentHour.Status != "unavailable" || missing.RecentHour.SnapshotRows != 0 {
		t.Fatalf("missing table classification = %+v", missing)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	// A view predicate deliberately exceeds the server statement timeout,
	// independently of whether the fixture connection is a superuser. The
	// savepoint rollback must leave the transaction usable.
	if _, err := admin.Exec(ctx, "DROP TABLE "+schema+".agent_telemetry"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE TABLE "+schema+".agent_telemetry_base (id uuid, agent_id uuid, observed_at timestamptz, payload jsonb)"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "INSERT INTO "+schema+`.agent_telemetry_base VALUES('50000000-0000-0000-0000-000000000010','60000000-0000-0000-0000-000000000010',$1,'{"metrics":[]}')`, anchor.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `CREATE FUNCTION `+schema+`.slow_true() RETURNS boolean LANGUAGE plpgsql VOLATILE AS $$ BEGIN PERFORM pg_sleep(0.25); RETURN true; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE VIEW "+schema+".agent_telemetry AS SELECT id,agent_id,observed_at,payload FROM "+schema+".agent_telemetry_base WHERE "+schema+".slow_true()"); err != nil {
		t.Fatal(err)
	}
	timeoutPool, err := openTimedPool(ctx, dsn, schema, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer timeoutPool.Close()
	tx, err = timeoutPool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer func(held pgx.Tx) { _ = held.Rollback(context.Background()) }(tx)
	if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout='100ms'"); err != nil {
		t.Fatal(err)
	}
	timedOut := observedVolumeReport(ctx, tx, anchor)
	if timedOut.Status != "unavailable" || timedOut.UnavailableReason == "" || timedOut.RecentHour.Status != "unavailable" || timedOut.RecentHour.SnapshotRows != 0 {
		t.Fatalf("timeout classification = %+v", timedOut)
	}
	var one int
	if err := tx.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("transaction unusable after savepoint recovery: value=%d err=%v", one, err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
}
