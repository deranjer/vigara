package alerts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRuleValidation(t *testing.T) {
	cases := []struct {
		name string
		q    ruleRequest
		ok   bool
	}{
		{"device threshold", ruleRequest{Name: "offline", Kind: "device_unavailable", Config: Config{StaleAfterSeconds: 30}}, true},
		{"threshold", ruleRequest{Name: "cpu", Kind: "threshold", Config: Config{Metric: "cpu_used_percent", Operator: "gte", Threshold: 90, SustainSeconds: 60}}, true},
		{"nan", ruleRequest{Name: "bad", Kind: "threshold", Config: Config{Metric: "cpu_used_percent", Operator: "gte", Threshold: math.NaN()}}, false},
		{"backup without policy", ruleRequest{Name: "backup", Kind: "backup_overdue", Config: Config{IntervalSeconds: 3600}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRule(tc.q)
			if (err == nil) != tc.ok {
				t.Fatalf("validateRule() error=%v, want valid=%v", err, tc.ok)
			}
		})
	}
}

func TestCompare(t *testing.T) {
	for _, tc := range []struct {
		op   string
		v, w float64
		want bool
	}{{"gt", 2, 1, true}, {"gte", 1, 1, true}, {"lt", 1, 2, true}, {"lte", 2, 2, true}, {"gt", 1, 1, false}} {
		if got := compare(tc.v, tc.op, tc.w); got != tc.want {
			t.Fatalf("compare(%g,%s,%g)=%v want %v", tc.v, tc.op, tc.w, got, tc.want)
		}
	}
}

func TestEvaluateLifecycleAndBackupEvidence(t *testing.T) {
	pool := alertsTestPool(t)
	ctx := context.Background()
	now := time.Now().UTC()
	var dev, agent string
	if err := pool.QueryRow(ctx, `INSERT INTO devices(display_name,last_seen_at) VALUES('alerts-test',$1) RETURNING id::text`, now.Add(-31*time.Second)).Scan(&dev); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("alerts-test-agent"))
	if err := pool.QueryRow(ctx, `INSERT INTO agent_identities(device_id,credential_hash,protocol_version,platform,architecture,agent_version,capabilities,allowed_capabilities) VALUES($1,$2,'v1','linux','amd64','test','[]','{}') RETURNING id::text`, dev, sum[:]).Scan(&agent); err != nil {
		t.Fatal(err)
	}
	insertRule(t, pool, "device", "device_unavailable", Config{DeviceID: dev, StaleAfterSeconds: 30, GraceSeconds: 5})
	insertRule(t, pool, "cpu", "threshold", Config{DeviceID: dev, Metric: "cpu_used_percent", Operator: "gte", Threshold: 90, SustainSeconds: 180})
	insertRule(t, pool, "memory grace", "threshold", Config{DeviceID: dev, Metric: "memory_used_percent", Operator: "gte", Threshold: 90, GraceSeconds: 5})
	insertRule(t, pool, "backup overdue", "backup_overdue", Config{DeviceID: dev, PolicyKey: "nightly", IntervalSeconds: 60, GraceSeconds: 10})
	insertRule(t, pool, "backup failure", "backup_failure", Config{DeviceID: dev, PolicyKey: "nightly"})
	for _, item := range []struct {
		at    time.Time
		value float64
	}{{now.Add(-5 * time.Minute), 95}, {now.Add(-3 * time.Minute), 92}, {now.Add(-time.Minute), 96}} {
		payload, _ := json.Marshal(map[string]any{"metrics": []any{map[string]any{"name": "cpu_used_percent", "value": item.value, "labels": map[string]string{"core": "all"}}}})
		id := item.at.Format(time.RFC3339Nano)
		if _, err := pool.Exec(ctx, `INSERT INTO agent_telemetry(agent_id,report_id,observed_at,payload) VALUES($1,$2,$3,$4)`, agent, id, item.at, payload); err != nil {
			t.Fatal(err)
		}
	}
	memPayload, _ := json.Marshal(map[string]any{"metrics": []any{map[string]any{"name": "memory_used_percent", "value": 95, "labels": map[string]string{"area": "host"}}}})
	if _, err := pool.Exec(ctx, `INSERT INTO agent_telemetry(agent_id,report_id,observed_at,payload) VALUES($1,'memory-high',$2,$3)`, agent, now, memPayload); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO backup_status(device_id,policy_key,status,observed_at,source_id) VALUES($1,'nightly','success',$2,'success-1')`, dev, now.Add(-69*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := Evaluate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	// Grace leaves the time-driven conditions pending; sustained CPU should fire from three distinct fresh points.
	assertCount(t, pool, `SELECT count(*) FROM alert_incidents WHERE status='firing'`, 1)
	time.Sleep(5200 * time.Millisecond)
	if err := Evaluate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	assertCount(t, pool, `SELECT count(*) FROM alert_incidents WHERE status='firing'`, 4)
	assertCount(t, pool, `SELECT count(*) FROM alert_events WHERE transition='firing'`, 4)
	if err := Evaluate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	assertCount(t, pool, `SELECT count(*) FROM alert_events WHERE transition='firing'`, 4)
	// A bad newest sample with an older healthy sample is pending, not recovery.
	at := time.Now().UTC().Add(-100 * time.Millisecond)
	healthyPayload, _ := json.Marshal(map[string]any{"metrics": []any{map[string]any{"name": "cpu_used_percent", "value": 20, "labels": map[string]string{"core": "all"}}}})
	badPayload, _ := json.Marshal(map[string]any{"metrics": []any{map[string]any{"name": "cpu_used_percent", "value": 95, "labels": map[string]string{"core": "all"}}}})
	if _, err := pool.Exec(ctx, `INSERT INTO agent_telemetry(agent_id,report_id,observed_at,payload) VALUES($1,'healthy-before-bad',$2,$3)`, agent, at.Add(-time.Second), healthyPayload); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agent_telemetry(agent_id,report_id,observed_at,payload) VALUES($1,'bad-after-healthy',$2,$3)`, agent, at, badPayload); err != nil {
		t.Fatal(err)
	}
	if err := Evaluate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	assertCount(t, pool, `SELECT count(*) FROM alert_incidents WHERE rule_id=(SELECT id FROM alert_rules WHERE name='cpu') AND status='firing'`, 1)
	// A subsequent fresh healthy sample resolves CPU; a running backup signal does not recover a failure.
	at = time.Now().UTC().Add(-100 * time.Millisecond)
	payload, _ := json.Marshal(map[string]any{"metrics": []any{map[string]any{"name": "cpu_used_percent", "value": 20, "labels": map[string]string{"core": "all"}}, map[string]any{"name": "memory_used_percent", "value": 20, "labels": map[string]string{"area": "host"}}}})
	if _, err := pool.Exec(ctx, `INSERT INTO agent_telemetry(agent_id,report_id,observed_at,payload) VALUES($1,'healthy-cpu',$2,$3)`, agent, at, payload); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO backup_status(device_id,policy_key,status,observed_at,source_id) VALUES($1,'nightly','failure',$2,'failure-1')`, dev, at); err != nil {
		t.Fatal(err)
	}
	if err := Evaluate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	assertCount(t, pool, `SELECT count(*) FROM alert_incidents WHERE status='firing'`, 3) // Device, overdue, and failure remain.
	if _, err := pool.Exec(ctx, `INSERT INTO backup_status(device_id,policy_key,status,observed_at,source_id) VALUES($1,'nightly','running',$2,'running-1')`, dev, time.Now().UTC().Add(-100*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if err := Evaluate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	assertCount(t, pool, `SELECT count(*) FROM alert_incidents WHERE rule_id=(SELECT id FROM alert_rules WHERE name='backup failure') AND status='firing'`, 1)
	if _, err := pool.Exec(ctx, `INSERT INTO backup_status(device_id,policy_key,status,observed_at,source_id) VALUES($1,'nightly','success',$2,'success-2')`, dev, time.Now().UTC().Add(-100*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if err := Evaluate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	assertCount(t, pool, `SELECT count(*) FROM alert_incidents WHERE rule_id=(SELECT id FROM alert_rules WHERE name='backup failure') AND status='resolved'`, 1)
	assertCount(t, pool, `SELECT count(*) FROM alert_events WHERE transition='resolved'`, 4)
}

func insertRule(t *testing.T, pool *pgxpool.Pool, name, kind string, c Config) {
	t.Helper()
	b, _ := json.Marshal(c)
	if _, err := pool.Exec(context.Background(), `INSERT INTO alert_rules(name,kind,config) VALUES($1,$2,$3)`, name, kind, b); err != nil {
		t.Fatal(err)
	}
}
func assertCount(t *testing.T, pool *pgxpool.Pool, q string, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(context.Background(), q).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("query count=%d want %d", got, want)
	}
}
func alertsTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("VIGIRA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set VIGIRA_TEST_DATABASE_URL to run PostgreSQL alert lifecycle tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	if _, err = admin.Exec(ctx, `BEGIN; SELECT pg_advisory_xact_lock(138139136); CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public; COMMIT;`); err != nil {
		t.Fatal(err)
	}
	schema := pgx.Identifier{fmt.Sprintf("alerts_test_%d", time.Now().UnixNano())}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
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
	for _, name := range []string{"000001_initial_schema.up.sql", "000003_agent_enrollment.up.sql", "000005_telemetry.up.sql", "000007_alerts.up.sql"} {
		migration, err := os.ReadFile("../../migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(migration)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	return pool
}

func TestBackupFailurePersistsWithoutFreshnessTimeout(t *testing.T) {
	pool := alertsTestPool(t)
	ctx := context.Background()
	var dev string
	if err := pool.QueryRow(ctx, `INSERT INTO devices(display_name) VALUES('backup-alert-test') RETURNING id::text`).Scan(&dev); err != nil {
		t.Fatal(err)
	}
	insertRule(t, pool, "archive failure", "backup_failure", Config{DeviceID: dev, PolicyKey: "archive"})
	at := time.Now().UTC().Add(-time.Hour)
	if _, err := pool.Exec(ctx, `INSERT INTO backup_status(device_id,policy_key,status,observed_at,source_id) VALUES($1,'archive','failure',$2,'old-failure')`, dev, at); err != nil {
		t.Fatal(err)
	}
	if err := Evaluate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	assertCount(t, pool, `SELECT count(*) FROM alert_incidents WHERE status='firing'`, 1)
	if _, err := pool.Exec(ctx, `INSERT INTO backup_status(device_id,policy_key,status,observed_at,source_id) VALUES($1,'archive','running',$2,'running')`, dev, time.Now().UTC().Add(-100*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if err := Evaluate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	assertCount(t, pool, `SELECT count(*) FROM alert_incidents WHERE status='firing'`, 1)
	if _, err := pool.Exec(ctx, `INSERT INTO backup_status(device_id,policy_key,status,observed_at,source_id) VALUES($1,'archive','success',$2,'success')`, dev, time.Now().UTC().Add(-100*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if err := Evaluate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	assertCount(t, pool, `SELECT count(*) FROM alert_incidents WHERE status='resolved'`, 1)
}

func TestBackupStatusIdempotencyAndAuthenticatedRules(t *testing.T) {
	pool := alertsTestPool(t)
	ctx := context.Background()
	var dev, agent string
	if err := pool.QueryRow(ctx, `INSERT INTO devices(display_name) VALUES('backup-ingest-test') RETURNING id::text`).Scan(&dev); err != nil {
		t.Fatal(err)
	}
	const credential = "backup-status-test-credential"
	sum := sha256.Sum256([]byte(credential))
	if err := pool.QueryRow(ctx, `INSERT INTO agent_identities(device_id,credential_hash,protocol_version,platform,architecture,agent_version,capabilities,allowed_capabilities) VALUES($1,$2,'v1','linux','amd64','test','[]','{}') RETURNING id::text`, dev, sum[:]).Scan(&agent); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewHandler(pool, nil).Register(mux)
	unauth := httptest.NewRecorder()
	mux.ServeHTTP(unauth, httptest.NewRequest(http.MethodGet, "/api/alerts/rules", nil))
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated rules request=%d", unauth.Code)
	}
	send := func(source, status string) int {
		body, _ := json.Marshal(BackupSignal{DeviceID: dev, PolicyKey: "daily", Status: status, ObservedAt: time.Now().UTC(), SourceID: source})
		req := httptest.NewRequest(http.MethodPost, "/api/agent/backups/status", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+credential)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w.Code
	}
	if code := send("event-1", "running"); code != http.StatusAccepted {
		t.Fatalf("first status=%d", code)
	}
	if code := send("event-1", "running"); code != http.StatusAccepted {
		t.Fatalf("retry status=%d", code)
	}
	if code := send("event-2", "success"); code != http.StatusAccepted {
		t.Fatalf("success status=%d", code)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM backup_status WHERE device_id=$1`, dev).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("stored %d backup events, want 2", count)
	}
}

func TestRuleFailureDoesNotBlockOtherRules(t *testing.T) {
	pool := alertsTestPool(t)
	ctx := context.Background()
	var dev, agent string
	now := time.Now().UTC()
	if err := pool.QueryRow(ctx, `INSERT INTO devices(display_name,last_seen_at) VALUES('savepoint-test',$1) RETURNING id::text`, now.Add(-2*time.Minute)).Scan(&dev); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("savepoint-test-agent"))
	if err := pool.QueryRow(ctx, `INSERT INTO agent_identities(device_id,credential_hash,protocol_version,platform,architecture,agent_version,capabilities,allowed_capabilities) VALUES($1,$2,'v1','linux','amd64','test','[]','{}') RETURNING id::text`, dev, sum[:]).Scan(&agent); err != nil {
		t.Fatal(err)
	}
	insertRule(t, pool, "device down", "device_unavailable", Config{DeviceID: dev, StaleAfterSeconds: 30})
	insertRule(t, pool, "bad metric", "threshold", Config{DeviceID: dev, Metric: "cpu_used_percent", Operator: "gt", Threshold: 90})
	if _, err := pool.Exec(ctx, `INSERT INTO agent_telemetry(agent_id,report_id,observed_at,payload) VALUES($1,'bad-json-number',$2,$3)`, agent, now.Add(-time.Second), `{"metrics":[{"name":"cpu_used_percent","value":"not-a-number"}]}`); err != nil {
		t.Fatal(err)
	}
	if err := Evaluate(ctx, pool); err == nil {
		t.Fatal("expected malformed metric to fail its rule evaluation")
	}
	assertCount(t, pool, `SELECT count(*) FROM alert_incidents WHERE status='firing'`, 1)
	assertCount(t, pool, `SELECT count(*) FROM alert_events WHERE transition='firing'`, 1)
}
