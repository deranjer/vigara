package telemetry

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/deranjer/vigara/internal/protocol"
)

func TestContainerChurnDoesNotBlockTelemetry(t *testing.T) {
	pool := telemetryTestPool(t)
	ctx := context.Background()
	var device, agent string
	if err := pool.QueryRow(ctx, "INSERT INTO devices(display_name) VALUES('container-churn') RETURNING id::text").Scan(&device); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO agent_identities(device_id,credential_hash,protocol_version,platform,architecture,agent_version,capabilities,allowed_capabilities) VALUES($1,decode('aabb','hex'),'v1','linux','amd64','test','[]','{}') RETURNING id::text`, device).Scan(&agent); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	// Reproduce an agent already at its old lifetime quota before upgrading.
	if _, err := pool.Exec(ctx, `INSERT INTO telemetry_metric_series(agent_id,metric_name,labels) SELECT $1,'docker_memory_usage_bytes',jsonb_build_object('container_id','retired-'||n) FROM generate_series(1,512) n`, agent); err != nil {
		t.Fatal(err)
	}
	snapshots := make([]protocol.TelemetrySnapshot, 3)
	for generation := range snapshots {
		snapshot := validSnapshot(agent, fmt.Sprintf("generation-%d", generation), now.Add(time.Duration(generation-2)*time.Minute))
		for container := 0; container < 32; container++ {
			for name, unit := range metricUnits {
				if len(name) < 7 || name[:7] != "docker_" {
					continue
				}
				snapshot.Metrics = append(snapshot.Metrics, protocol.TelemetryMetric{Name: name, Unit: unit, Value: 1, Labels: map[string]string{"container_id": fmt.Sprintf("%064x", generation*32+container)}})
			}
		}
		if err := validate(snapshot, agent, now); err != nil {
			t.Fatal(err)
		}
		snapshots[generation] = snapshot
	}
	store := postgresStore{pool: pool}
	if err := store.Ingest(ctx, agent, snapshots, now); err != nil {
		t.Fatalf("container recreation blocked fresh telemetry: %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM telemetry_metric_series WHERE agent_id=$1", agent).Scan(&count); err != nil || count <= 512 {
		t.Fatalf("historical series count=%d err=%v", count, err)
	}
	var report string
	if err := pool.QueryRow(ctx, "SELECT report_id FROM agent_telemetry_latest WHERE agent_id=$1", agent).Scan(&report); err != nil || report != "generation-2" {
		t.Fatalf("latest=%s err=%v", report, err)
	}
	// A different older report must not regress shared series freshness.
	older := validSnapshot(agent, "late-report", now.Add(-time.Hour))
	if err := store.Ingest(ctx, agent, []protocol.TelemetrySnapshot{older}, now); err != nil {
		t.Fatal(err)
	}
	var observed time.Time
	if err := pool.QueryRow(ctx, `SELECT last_observed_at FROM telemetry_metric_series WHERE agent_id=$1 AND metric_name='memory_total_bytes' AND labels='{}'`, agent).Scan(&observed); err != nil || !observed.Equal(now) {
		t.Fatalf("freshness regressed to %s: %v", observed, err)
	}
	// Replaying accepted snapshots remains idempotent.
	if err := store.Ingest(ctx, agent, snapshots, now); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM agent_telemetry WHERE agent_id=$1", agent).Scan(&count); err != nil || count != 4 {
		t.Fatalf("raw rows=%d err=%v", count, err)
	}
	// Expired series metadata is pruned without dropping retained history.
	if _, err := pool.Exec(ctx, `INSERT INTO telemetry_metric_series(agent_id,metric_name,labels,last_observed_at) VALUES($1,'network_check_success','{"target":"expired"}',$2)`, agent, now.Add(-91*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.Retain(ctx, RetentionConfig{RawAge: 25 * time.Hour, RollupAge: 90 * 24 * time.Hour}, now); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM telemetry_metric_series WHERE agent_id=$1 AND metric_name='network_check_success'`, agent).Scan(&count); err != nil || count != 0 {
		t.Fatalf("expired series retained: %d %v", count, err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM agent_telemetry WHERE agent_id=$1", agent).Scan(&count); err != nil || count != 4 {
		t.Fatalf("cleanup lost retained history: %d %v", count, err)
	}
}

func TestOversizedSnapshotRejectedBeforeStorage(t *testing.T) {
	snapshot := protocol.TelemetrySnapshot{Metrics: make([]protocol.TelemetryMetric, maxMetricsPerSnapshot+1)}
	if err := (postgresStore{}).Ingest(context.Background(), "unused", []protocol.TelemetrySnapshot{snapshot}, time.Now()); err != ErrSeriesLimit {
		t.Fatalf("oversized snapshot error: %v", err)
	}
}
