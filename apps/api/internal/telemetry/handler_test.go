package telemetry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
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
	snapshot := validSnapshot("agent", "snapshot-1", now.Add(-365*24*time.Hour))
	if err := validate(snapshot, "agent", now); err != nil {
		t.Fatalf("valid offline snapshot rejected: %v", err)
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
	snapshot := validSnapshot(agentID, "stable-snapshot-id", time.Now().UTC().Add(-90*24*time.Hour))
	body, err := json.Marshal(protocol.TelemetrySnapshotBatch{ProtocolVersion: protocol.CurrentVersion, AgentID: agentID, Snapshots: []protocol.TelemetrySnapshot{snapshot}})
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
	if count != 1 {
		t.Fatalf("stored %d rows after idempotent retry, want 1", count)
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
	return pool
}
