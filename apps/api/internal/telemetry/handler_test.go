package telemetry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/deranjer/vigara/internal/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestValidateRejectsBadUnitsAndFutureObservationButAllowsOfflineData(t *testing.T) {
	now := time.Now().UTC()
	snapshot := validSnapshot("agent", "snapshot-1", now.Add(-47*time.Hour))
	if err := validate(snapshot, "agent", now); err != nil {
		t.Fatalf("valid offline snapshot rejected: %v", err)
	}
	tooOld := validSnapshot("agent", "old", now.Add(-26*time.Hour))
	if err := validate(tooOld, "agent", now); err != nil {
		t.Fatalf("stale report should be acknowledged and discarded after validation: %v", err)
	}
	snapshot.Metrics = []protocol.TelemetryMetric{
		{Name: "network_check_success", Value: 1, Unit: "1", Labels: map[string]string{"check_name": "router-health", "check_type": "http"}},
		{Name: "network_check_latency_milliseconds", Value: 2.5, Unit: "ms", Labels: map[string]string{"check_name": "router-health", "check_type": "http"}},
	}
	if err := validate(snapshot, "agent", now); err != nil {
		t.Fatalf("valid optional network-check telemetry rejected: %v", err)
	}
	snapshot.Metrics = []protocol.TelemetryMetric{{Name: "memory_total_bytes", Value: 10, Unit: "percent"}}
	if err := validate(snapshot, "agent", now); err == nil {
		t.Fatal("accepted a metric with a mismatched unit")
	}
	snapshot = validSnapshot("agent", "snapshot-2", now.Add(6*time.Minute))
	if err := validate(snapshot, "agent", now); err == nil {
		t.Fatal("accepted future observation")
	}
	snapshot = validSnapshot("agent", "snapshot-3", now)
	snapshot.Metrics = []protocol.TelemetryMetric{{Name: "filesystem_total_bytes", Value: 10, Unit: "By", Labels: map[string]string{"mount_path": string(bytes.Repeat([]byte("x"), 257))}}}
	if err := validate(snapshot, "agent", now); err == nil {
		t.Fatal("accepted oversized label")
	}
}

func TestValidateAcceptsBoundedDockerInventoryAndUnits(t *testing.T) {
	now := time.Now().UTC()
	s := validSnapshot("agent", "docker", now)
	s.DockerContainers = []protocol.DockerContainer{{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Name: "web", Image: "demo:latest", State: "running"}}
	s.Metrics = []protocol.TelemetryMetric{{Name: "docker_memory_usage_bytes", Value: 1024, Unit: "By", Labels: map[string]string{"container_id": s.DockerContainers[0].ID}}, {Name: "docker_cpu_capacity_used_percent", Value: 25, Unit: "%", Labels: map[string]string{"container_id": s.DockerContainers[0].ID}}}
	if err := validate(s, "agent", now); err != nil {
		t.Fatalf("valid Docker snapshot rejected: %v", err)
	}
	s.DockerContainers = append(s.DockerContainers, s.DockerContainers[0])
	if err := validate(s, "agent", now); err == nil {
		t.Fatal("accepted duplicate Docker container IDs")
	}
}

func TestValidateSynologyMetricsRequireTargetAndSupportedSchema(t *testing.T) {
	now := time.Now().UTC()
	s := validSnapshot("agent", "synology", now)
	s.Metrics = []protocol.TelemetryMetric{
		{Name: "synology_system_status", Value: 1, Unit: "1", Labels: map[string]string{"nas_target": "nas.example"}},
		{Name: "synology_temperature_celsius", Value: 42, Unit: "Cel", Labels: map[string]string{"nas_target": "nas.example"}},
		{Name: "synology_disk_status", Value: 1, Unit: "1", Labels: map[string]string{"nas_target": "nas.example", "disk_index": "1"}},
		{Name: "synology_disk_temperature_celsius", Value: 34, Unit: "Cel", Labels: map[string]string{"nas_target": "nas.example", "disk_index": "1"}},
	}
	if err := validate(s, "agent", now); err != nil {
		t.Fatalf("valid Synology metrics rejected: %v", err)
	}
	s.Metrics[0].Labels = nil
	if err := validate(s, "agent", now); err == nil {
		t.Fatal("accepted a Synology metric without its remote target label")
	}
	s = validSnapshot("agent", "synology-bad-unit", now)
	s.Metrics = []protocol.TelemetryMetric{{Name: "synology_temperature_celsius", Value: 42, Unit: "C", Labels: map[string]string{"nas_target": "nas.example"}}}
	if err := validate(s, "agent", now); err == nil {
		t.Fatal("accepted unsupported Synology unit")
	}
	s = validSnapshot("agent", "synology-bad-status", now)
	s.Metrics = []protocol.TelemetryMetric{{Name: "synology_system_status", Value: 7, Unit: "1", Labels: map[string]string{"nas_target": "nas.example"}}}
	if err := validate(s, "agent", now); err == nil {
		t.Fatal("accepted unsupported Synology status")
	}
	s = validSnapshot("agent", "synology-fractional-status", now)
	s.Metrics = []protocol.TelemetryMetric{{Name: "synology_system_status", Value: 1.5, Unit: "1", Labels: map[string]string{"nas_target": "nas.example"}}}
	if err := validate(s, "agent", now); err == nil {
		t.Fatal("accepted fractional Synology status")
	}
	s = validSnapshot("agent", "synology-bad-index", now)
	s.Metrics = []protocol.TelemetryMetric{{Name: "synology_disk_status", Value: 1, Unit: "1", Labels: map[string]string{"nas_target": "nas.example", "disk_index": "+1"}}}
	if err := validate(s, "agent", now); err == nil {
		t.Fatal("accepted nondecimal Synology disk index")
	}
}

func TestTelemetryIngestIsIdempotentAndRejectsRevokedIdentity(t *testing.T) {
	pool := telemetryTestPool(t)
	ctx := context.Background()
	const credential = "telemetry-test-credential"
	deviceID := ""
	if err := pool.QueryRow(ctx, `INSERT INTO devices(display_name) VALUES('telemetry-test') RETURNING id::text`).Scan(&deviceID); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(credential))
	var agentID string
	if err := pool.QueryRow(ctx, `INSERT INTO agent_identities(device_id, credential_hash, protocol_version, platform, architecture, agent_version, capabilities, allowed_capabilities) VALUES($1,$2,'v1','linux','amd64','test','[]','{}') RETURNING id::text`, deviceID, sum[:]).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewHandler(pool).Register(mux)
	snapshots := make([]protocol.TelemetrySnapshot, 5)
	for i := range snapshots {
		snapshots[i] = validSnapshot(agentID, fmt.Sprintf("stable-snapshot-id-%d", i), time.Now().UTC().Add(-time.Hour))
	}
	body, err := json.Marshal(protocol.TelemetrySnapshotBatch{ProtocolVersion: protocol.CurrentVersion, AgentID: agentID, Snapshots: snapshots})
	if err != nil {
		t.Fatal(err)
	}
	request := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/agent/telemetry", bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+credential)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	for i := 0; i < 2; i++ {
		if w := request(); w.Code != http.StatusAccepted {
			t.Fatalf("ingest attempt %d returned %d: %s", i, w.Code, w.Body.String())
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_telemetry WHERE agent_id=$1`, agentID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 5 {
		t.Fatalf("stored %d rows after idempotent batch retry, want 5", count)
	}
	var lastSeen time.Time
	if err := pool.QueryRow(ctx, `SELECT last_seen_at FROM devices WHERE id=$1`, deviceID).Scan(&lastSeen); err != nil {
		t.Fatal(err)
	}
	if time.Since(lastSeen) > time.Minute {
		t.Fatalf("last_seen_at was not updated from server receipt: %s", lastSeen)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_identities SET revoked_at=now() WHERE id=$1`, agentID); err != nil {
		t.Fatal(err)
	}
	if w := request(); w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked identity returned %d: %s", w.Code, w.Body.String())
	}
}

func TestDockerInventorySnapshotIngestsAndPersists(t *testing.T) {
	pool := telemetryTestPool(t)
	ctx := context.Background()
	const credential = "docker-telemetry-test-credential"
	var deviceID, agentID string
	if err := pool.QueryRow(ctx, `INSERT INTO devices(display_name) VALUES('docker-telemetry-test') RETURNING id::text`).Scan(&deviceID); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(credential))
	if err := pool.QueryRow(ctx, `INSERT INTO agent_identities(device_id,credential_hash,protocol_version,platform,architecture,agent_version,capabilities,allowed_capabilities) VALUES($1,$2,'v1','linux','amd64','test','[]','{}') RETURNING id::text`, deviceID, sum[:]).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	snapshot := validSnapshot(agentID, "docker-inventory-snapshot", time.Now().UTC())
	snapshot.DockerContainers = []protocol.DockerContainer{{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Name: "web", Image: "demo:latest", State: "running"}}
	snapshot.Capabilities = append(snapshot.Capabilities, "docker_inventory", "docker_container_resources")
	snapshot.Metrics = []protocol.TelemetryMetric{{Name: "docker_memory_usage_bytes", Value: 4096, Unit: "By", Labels: map[string]string{"container_id": snapshot.DockerContainers[0].ID}}, {Name: "docker_cpu_capacity_used_percent", Value: 17, Unit: "%", Labels: map[string]string{"container_id": snapshot.DockerContainers[0].ID}}}
	body, err := json.Marshal(protocol.TelemetrySnapshotBatch{ProtocolVersion: protocol.CurrentVersion, AgentID: agentID, Snapshots: []protocol.TelemetrySnapshot{snapshot}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewHandler(pool).Register(mux)
	request := httptest.NewRequest(http.MethodPost, "/api/agent/telemetry", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+credential)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("Docker snapshot ingest returned %d: %s", response.Code, response.Body.String())
	}
	var payload []byte
	if err := pool.QueryRow(ctx, `SELECT payload FROM agent_telemetry_latest WHERE agent_id=$1`, agentID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var stored protocol.TelemetrySnapshot
	if err := json.Unmarshal(payload, &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.DockerContainers) != 1 || stored.DockerContainers[0].Name != "web" || len(stored.Metrics) != 2 {
		t.Fatalf("persisted latest snapshot lost Docker inventory or metrics: %+v", stored)
	}
}

func validSnapshot(agentID, id string, observedAt time.Time) protocol.TelemetrySnapshot {
	return protocol.TelemetrySnapshot{ProtocolVersion: protocol.CurrentVersion, ID: id, AgentID: agentID, ObservedAt: observedAt, Host: protocol.HostSummary{OS: "linux", Architecture: "amd64", AgentVersion: "test"}, Metrics: []protocol.TelemetryMetric{{Name: "memory_total_bytes", Value: 1024, Unit: "By"}}, Capabilities: []string{"memory"}}
}

func telemetryTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("VIGIRA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set VIGIRA_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	if _, err := admin.Exec(ctx, `BEGIN; SELECT pg_advisory_xact_lock(138139136); CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public; COMMIT;`); err != nil {
		t.Fatal(err)
	}
	schema := pgx.Identifier{fmt.Sprintf("telemetry_test_%d", time.Now().UnixNano())}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	// Deliberately apply 000005 without inventory's 000004 migration.
	for _, name := range []string{"000001_initial_schema.up.sql", "000003_agent_enrollment.up.sql", "000005_telemetry.up.sql"} {
		migration, err := os.ReadFile("../../migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(migration)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	var seedDevice, seedAgent string
	if err := pool.QueryRow(ctx, `INSERT INTO devices(display_name) VALUES('migration-seed') RETURNING id::text`).Scan(&seedDevice); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO agent_identities(device_id,credential_hash,protocol_version,platform,architecture,agent_version,capabilities,allowed_capabilities) VALUES($1,decode('aabb','hex'),'v1','linux','amd64','test','[]','{}') RETURNING id::text`, seedDevice).Scan(&seedAgent); err != nil {
		t.Fatal(err)
	}
	seedPayload := `{"id":"migration-seed","agentId":"` + seedAgent + `","observedAt":"` + time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano) + `","metrics":[{"name":"memory_total_bytes","value":4096,"unit":"By"}]}`
	if _, err := pool.Exec(ctx, `INSERT INTO agent_telemetry(agent_id,report_id,observed_at,payload) VALUES($1,'migration-seed',now()-interval '1 hour',$2::jsonb)`, seedAgent, seedPayload); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../migrations/000006_telemetry_retention.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(migration)); err != nil {
		t.Fatalf("apply 000006_telemetry_retention.up.sql: %v", err)
	}
	return pool
}

func TestRetentionMigrationSeedsDedupSeriesAndLatestSnapshot(t *testing.T) {
	pool := telemetryTestPool(t)
	ctx := context.Background()
	var agentID string
	if err := pool.QueryRow(ctx, `SELECT agent_id::text FROM agent_telemetry WHERE report_id='migration-seed'`).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	var latestID string
	if err := pool.QueryRow(ctx, `SELECT report_id FROM agent_telemetry_latest WHERE agent_id=$1`, agentID).Scan(&latestID); err != nil {
		t.Fatal(err)
	}
	if latestID != "migration-seed" {
		t.Fatalf("seeded latest report = %q", latestID)
	}
	var dedup, legacy bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM telemetry_report_dedup WHERE agent_id=$1 AND report_id='migration-seed'), EXISTS(SELECT 1 FROM telemetry_metric_series WHERE agent_id=$1 AND metric_name='memory_total_bytes' AND legacy)`, agentID).Scan(&dedup, &legacy); err != nil {
		t.Fatal(err)
	}
	if !dedup || !legacy {
		t.Fatalf("migration seed dedup=%v legacy_series=%v", dedup, legacy)
	}
}

func TestTelemetryLimitsAndRetentionAreBoundedAndIdempotent(t *testing.T) {
	pool := telemetryTestPool(t)
	ctx := context.Background()
	var deviceID, agentID string
	if err := pool.QueryRow(ctx, `INSERT INTO devices(display_name) VALUES('retention-test') RETURNING id::text`).Scan(&deviceID); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("retention-test-credential"))
	if err := pool.QueryRow(ctx, `INSERT INTO agent_identities(device_id,credential_hash,protocol_version,platform,architecture,agent_version,capabilities,allowed_capabilities) VALUES($1,$2,'v1','linux','amd64','test','[]','{}') RETURNING id::text`, deviceID, sum[:]).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	store := postgresStore{pool: pool}
	now := time.Now().UTC()
	var err error
	batch := make([]protocol.TelemetrySnapshot, 10)
	for i := range batch {
		batch[i] = validSnapshot(agentID, fmt.Sprintf("rate-%d", i), now.Add(-time.Minute))
	}
	if err := store.Ingest(ctx, agentID, batch, now); err != nil {
		t.Fatalf("first batch: %v", err)
	}
	if err := store.Ingest(ctx, agentID, []protocol.TelemetrySnapshot{validSnapshot(agentID, "rate-11", now)}, now); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("second batch error = %v, want rate limit", err)
	}

	stale := validSnapshot(agentID, "stale-report", now.Add(-49*time.Hour))
	if err := store.Ingest(ctx, agentID, []protocol.TelemetrySnapshot{stale}, now.Add(time.Minute)); err != nil {
		t.Fatalf("stale report should be acknowledged and discarded: %v", err)
	}
	var staleRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_telemetry WHERE agent_id=$1 AND report_id='stale-report'`, agentID).Scan(&staleRows); err != nil {
		t.Fatal(err)
	}
	if staleRows != 0 {
		t.Fatalf("stored %d reports older than the acceptance horizon", staleRows)
	}
	seriesBatch := validSnapshot(agentID, "series-overflow", now.Add(time.Minute))
	for i := 0; i < 512; i++ {
		seriesBatch.Metrics = append(seriesBatch.Metrics, protocol.TelemetryMetric{Name: "filesystem_total_bytes", Value: 1, Unit: "By", Labels: map[string]string{"mount_path": fmt.Sprintf("/series/%d", i)}})
	}
	if err := store.Ingest(ctx, agentID, []protocol.TelemetrySnapshot{seriesBatch}, now.Add(2*time.Minute)); !errors.Is(err, ErrSeriesLimit) {
		t.Fatalf("series overflow error = %v, want limit", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_telemetry WHERE agent_id=$1 AND report_id='series-overflow'`, agentID).Scan(&staleRows); err != nil {
		t.Fatal(err)
	}
	if staleRows != 0 {
		t.Fatal("series-limit rejection partially committed raw telemetry")
	}
	old := validSnapshot(agentID, "old-report", now.Add(-26*time.Hour))
	for i := 0; i < 601; i++ {
		id := fmt.Sprintf("old-report-%d", i)
		if i == 0 {
			old.ID = id
		}
		snapshot := validSnapshot(agentID, id, old.ObservedAt)
		row, marshalErr := json.Marshal(snapshot)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if _, err = pool.Exec(ctx, `INSERT INTO telemetry_report_dedup(agent_id,report_id,received_at) VALUES($1,$2,$3)`, agentID, id, now.Add(-26*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, `INSERT INTO agent_telemetry(agent_id,report_id,observed_at,payload) VALUES($1,$2,$3,$4)`, agentID, id, old.ObservedAt, row); err != nil {
			t.Fatal(err)
		}
	}
	if err = store.Retain(ctx, RetentionConfig{RawAge: 25 * time.Hour, RollupAge: 90 * 24 * time.Hour}, now); err != nil {
		t.Fatal(err)
	}
	var rawCount int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM agent_telemetry WHERE agent_id=$1 AND report_id=$2`, agentID, old.ID).Scan(&rawCount); err != nil {
		t.Fatal(err)
	}
	if rawCount != 0 {
		t.Fatalf("old report still has %d raw rows", rawCount)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM agent_telemetry WHERE agent_id=$1 AND observed_at < $2`, agentID, now.Add(-48*time.Hour)).Scan(&rawCount); err != nil {
		t.Fatal(err)
	}
	if rawCount != 0 {
		t.Fatalf("bounded worker left %d of 601 eligible raw rows", rawCount)
	}
	var rolledCount int64
	if err = pool.QueryRow(ctx, `SELECT sample_count FROM telemetry_metric_rollups WHERE agent_id=$1 AND metric_name='memory_total_bytes' AND bucket_start=$2`, agentID, old.ObservedAt.UTC().Truncate(time.Hour)).Scan(&rolledCount); err != nil {
		t.Fatal(err)
	}
	if rolledCount != 601 {
		t.Fatalf("rollup sample count = %d, want 601", rolledCount)
	}
	if err = store.Retain(ctx, RetentionConfig{RawAge: 25 * time.Hour, RollupAge: 90 * 24 * time.Hour}, now); err != nil {
		t.Fatal(err)
	}
	var after int64
	if err = pool.QueryRow(ctx, `SELECT sample_count FROM telemetry_metric_rollups WHERE agent_id=$1 AND metric_name='memory_total_bytes' AND bucket_start=$2`, agentID, old.ObservedAt.UTC().Truncate(time.Hour)).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != rolledCount {
		t.Fatalf("repeated retention changed sample count from %d to %d", rolledCount, after)
	}
	if err = store.Ingest(ctx, agentID, []protocol.TelemetrySnapshot{validSnapshot(agentID, old.ID, old.ObservedAt)}, now.Add(time.Minute)); err != nil {
		t.Fatalf("late duplicate retry: %v", err)
	}
	if err = pool.QueryRow(ctx, `SELECT sample_count FROM telemetry_metric_rollups WHERE agent_id=$1 AND metric_name='memory_total_bytes' AND bucket_start=$2`, agentID, old.ObservedAt.UTC().Truncate(time.Hour)).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != rolledCount {
		t.Fatalf("late duplicate changed rolled count from %d to %d", rolledCount, after)
	}
	var latestCount int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM agent_telemetry_latest WHERE agent_id=$1`, agentID).Scan(&latestCount); err != nil {
		t.Fatal(err)
	}
	if latestCount != 1 {
		t.Fatalf("latest host snapshot count = %d, want 1", latestCount)
	}
}

func TestRetentionConfigRequiresAlertCompatibleRawWindow(t *testing.T) {
	base := RetentionConfig{RollupAge: 90 * 24 * time.Hour}
	base.RawAge = 24 * time.Hour
	if err := base.Validate(); err == nil {
		t.Fatal("accepted a 24-hour raw window, below the alert freshness margin")
	}
	base.RawAge = 25 * time.Hour
	if err := base.Validate(); err != nil {
		t.Fatalf("rejected the 25-hour minimum raw window: %v", err)
	}
	base.RawAge = 30 * 24 * time.Hour
	base.RollupAge = 365 * 24 * time.Hour
	if err := base.Validate(); err != nil {
		t.Fatalf("rejected monthly raw/yearly summary policy: %v", err)
	}
	base.RawAge += time.Hour
	if err := base.Validate(); err == nil {
		t.Fatal("accepted raw retention above 30 days")
	}
	base.RawAge = 30 * 24 * time.Hour
	base.RollupAge += time.Hour
	if err := base.Validate(); err == nil {
		t.Fatal("accepted summary retention above 365 days")
	}
}
