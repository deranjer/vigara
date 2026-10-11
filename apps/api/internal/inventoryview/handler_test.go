package inventoryview

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/apps/api/internal/db"
	"github.com/deranjer/vigara/apps/api/internal/telemetry"
	"github.com/deranjer/vigara/internal/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestInventoryAndHistoryAreAuthenticatedBoundedAndPreserveGaps(t *testing.T) {
	pool := inventoryTestPool(t)
	ctx := context.Background()
	service := auth.NewService(pool, db.New(pool))
	session, err := service.CreateFirstUser(ctx, "inventory-admin", "Inventory Admin", "correct horse battery", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewHandler(pool, service).Register(mux)
	request := func(path string, cookie bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if cookie {
			r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session.Token})
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	if got := request("/api/inventory/devices", false).Code; got != http.StatusUnauthorized {
		t.Fatalf("anonymous list status=%d", got)
	}
	foreign := httptest.NewRequest(http.MethodGet, "/api/inventory/devices", nil)
	foreign.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session.Token})
	foreign.Header.Set("Origin", "https://attacker.invalid")
	denied := httptest.NewRecorder()
	mux.ServeHTTP(denied, foreign)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("foreign origin status=%d", denied.Code)
	}
	var healthy string
	now := time.Now().UTC()
	for _, item := range []struct {
		name   string
		seen   time.Time
		report string
		errors string
	}{{"healthy", now, "healthy", "[]"}, {"stale", now.Add(-time.Hour), "stale", "[]"}, {"failed", now, "failed", `[{"capability":"memory","message":"read failed"}]`}} {
		if err = pool.QueryRow(ctx, `INSERT INTO devices(display_name,last_seen_at) VALUES($1,$2) RETURNING id::text`, item.name, item.seen).Scan(new(string)); err != nil {
			t.Fatal(err)
		}
		var deviceID, agentID string
		if err = pool.QueryRow(ctx, `SELECT id::text FROM devices WHERE display_name=$1`, item.name).Scan(&deviceID); err != nil {
			t.Fatal(err)
		}
		if err = pool.QueryRow(ctx, `INSERT INTO agent_identities(device_id,credential_hash,protocol_version,platform,architecture,agent_version,capabilities,allowed_capabilities) VALUES($1,decode(md5($2),'hex'),'v1','linux','amd64','test','["memory"]','{}') RETURNING id::text`, deviceID, "agent-"+deviceID).Scan(&agentID); err != nil {
			t.Fatal(err)
		}
		if item.name == "healthy" {
			healthy = deviceID
		}
		payload := fmt.Sprintf(`{"observedAt":%q,"host":{"os":"linux"},"errors":%s,"metrics":[{"name":"memory_total_bytes","value":0,"unit":"By"}]}`, item.seen.Format(time.RFC3339Nano), item.errors)
		if _, err = pool.Exec(ctx, `INSERT INTO agent_telemetry_latest(agent_id,report_id,observed_at,payload,received_at) VALUES($1,$2,$3,$4::jsonb,$3)`, agentID, item.report, item.seen, payload); err != nil {
			t.Fatal(err)
		}
		if item.name == "healthy" {
			for idx, offset := range []time.Duration{12 * time.Minute, 5 * time.Minute} {
				observed := now.Add(-offset)
				raw := fmt.Sprintf(`{"metrics":[{"name":"memory_total_bytes","value":%d,"unit":"By"}]}`, 100+idx)
				if _, err = pool.Exec(ctx, `INSERT INTO agent_telemetry(agent_id,report_id,observed_at,payload) VALUES($1,$2,$3,$4::jsonb)`, agentID, fmt.Sprintf("raw-%d", idx), observed, raw); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = pool.Exec(ctx, `INSERT INTO telemetry_metric_series(agent_id,metric_name,labels) VALUES($1,'memory_total_bytes','{}')`, agentID); err != nil {
				t.Fatal(err)
			}
			bucket := now.Add(-2 * time.Hour).Truncate(time.Hour)
			if _, err = pool.Exec(ctx, `INSERT INTO agent_telemetry(agent_id,report_id,observed_at,payload) VALUES($1,'raw-rollup-overlap',$2,$3::jsonb)`, agentID, bucket.Add(30*time.Second), `{"metrics":[{"name":"memory_total_bytes","value":200,"unit":"By"}]}`); err != nil {
				t.Fatal(err)
			}
			if _, err = pool.Exec(ctx, `INSERT INTO agent_telemetry(agent_id,report_id,observed_at,payload) VALUES($1,'future-sample',$2,$3::jsonb)`, agentID, now.Add(time.Hour), `{"metrics":[{"name":"memory_total_bytes","value":999,"unit":"By"}]}`); err != nil {
				t.Fatal(err)
			}
			if _, err = pool.Exec(ctx, `INSERT INTO telemetry_metric_rollups(agent_id,metric_name,labels,bucket_start,sample_count,sum_value,min_value,max_value) VALUES($1,'memory_total_bytes','{}',$2,2,100,40,60)`, agentID, bucket); err != nil {
				t.Fatal(err)
			}
			revokedAgent := ""
			if err = pool.QueryRow(ctx, `INSERT INTO agent_identities(device_id,credential_hash,protocol_version,revoked_at) VALUES($1,decode(md5($2),'hex'),'v1',now()) RETURNING id::text`, deviceID, "revoked-"+deviceID).Scan(&revokedAgent); err != nil {
				t.Fatal(err)
			}
			if _, err = pool.Exec(ctx, `INSERT INTO telemetry_metric_series(agent_id,metric_name,labels) VALUES($1,'revoked_only_metric','{}')`, revokedAgent); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err = pool.Exec(ctx, `INSERT INTO devices(display_name) VALUES('unsupported')`); err != nil {
		t.Fatal(err)
	}
	list := request("/api/inventory/devices?limit=2&offset=0", true)
	if list.Code != 200 {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body)
	}
	var page DevicePage
	if err = json.Unmarshal(list.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 4 || page.Limit != 2 || len(page.Devices) != 2 {
		t.Fatalf("unexpected device page: %+v", page)
	}
	all := request("/api/inventory/devices?limit=100", true)
	if all.Code != 200 {
		t.Fatalf("all list status=%d", all.Code)
	}
	if err = json.Unmarshal(all.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, device := range page.Devices {
		states[device.Name] = device.Status
	}
	if states["healthy"] != "healthy" || states["stale"] != "stale" || states["failed"] != "failed" || states["unsupported"] != "unsupported" {
		t.Fatalf("wrong availability states: %#v", states)
	}
	for _, device := range page.Devices {
		if device.Name == "healthy" {
			var latest struct {
				Metrics []struct {
					Value float64 `json:"value"`
				} `json:"metrics"`
			}
			if err = json.Unmarshal(device.Latest, &latest); err != nil {
				t.Fatal(err)
			}
			if len(latest.Metrics) == 0 || latest.Metrics[0].Value != 0 {
				t.Fatalf("zero measurement was lost or rewritten: %+v", latest)
			}
		}
	}
	if got := request("/api/inventory/devices?limit=101", true).Code; got != 400 {
		t.Fatalf("oversized page status=%d", got)
	}
	if got := request("/api/inventory/devices/not-a-uuid/history?range=24h", true).Code; got != 400 {
		t.Fatalf("malformed ID status=%d", got)
	}
	if got := request("/api/inventory/devices/00000000-0000-0000-0000-000000000000/history", true).Code; got != 400 {
		t.Fatalf("missing range status=%d", got)
	}
	if got := request("/api/inventory/devices/00000000-0000-0000-0000-000000000000/history?range=24h", true).Code; got != 404 {
		t.Fatalf("missing device status=%d", got)
	}
	history := request("/api/inventory/devices/"+healthy+"/history?range=24h&metric=memory_total_bytes", true)
	if history.Code != 200 {
		t.Fatalf("history status=%d body=%s", history.Code, history.Body)
	}
	var result History
	if err = json.Unmarshal(history.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Series) != 1 || len(result.Series[0].Points) != 3 {
		t.Fatalf("expected three observed buckets with no future point, got %+v", result.Series)
	}
	if result.Series[0].Points[0].Samples != 3 || *result.Series[0].Points[0].Value != 100 || *result.Series[0].Points[0].Min != 40 || *result.Series[0].Points[0].Max != 200 {
		t.Fatalf("raw/rollup overlap was not weighted and combined correctly: %+v", result.Series[0].Points[0])
	}
	if len(result.Metrics) != 1 || result.Metrics[0] != "memory_total_bytes" {
		t.Fatalf("revoked-only metric leaked into inventory: %#v", result.Metrics)
	}
	for _, point := range result.Series[0].Points {
		if point.Value == nil {
			t.Fatal("missing observation was represented with a nil/zero value")
		}
		if point.At.After(time.Now().Add(time.Second)) {
			t.Fatalf("future point returned: %s", point.At)
		}
	}
}

func TestHistoryAfterRetentionCombinesRecentRawAndMultipleRollupBuckets(t *testing.T) {
	pool := inventoryTestPool(t)
	ctx := context.Background()
	service := auth.NewService(pool, db.New(pool))
	session, err := service.CreateFirstUser(ctx, "retention-admin", "Retention Admin", "correct horse battery", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	var deviceID, agentID string
	if err = pool.QueryRow(ctx, `INSERT INTO devices(display_name) VALUES('retention history') RETURNING id::text`).Scan(&deviceID); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `INSERT INTO agent_identities(device_id,credential_hash,protocol_version,platform,architecture,agent_version) VALUES($1,decode(md5($2),'hex'),'v1','linux','amd64','test') RETURNING id::text`, deviceID, "retention-agent").Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	snapshots := []protocol.TelemetrySnapshot{}
	for i, seen := range []time.Time{now.Add(-26*time.Hour - 20*time.Minute), now.Add(-25*time.Hour - 20*time.Minute), now.Add(-24*time.Hour - 20*time.Minute), now.Add(-5 * time.Minute), now.Add(-time.Minute)} {
		snapshots = append(snapshots, protocol.TelemetrySnapshot{ID: fmt.Sprintf("retained-history-%d", i), AgentID: agentID, ObservedAt: seen, Metrics: []protocol.TelemetryMetric{{Name: "cpu_used_percent", Value: float64(i + 10), Unit: "%", Labels: map[string]string{"core": "all"}}}})
	}
	store := telemetry.NewPostgresStore(pool)
	if err = store.Ingest(ctx, agentID, snapshots, now); err != nil {
		t.Fatalf("ingest history fixture: %v", err)
	}
	if err = store.Retain(ctx, telemetry.RetentionConfig{RawAge: 25 * time.Hour, RollupAge: 90 * 24 * time.Hour}, now); err != nil {
		t.Fatalf("retain history fixture: %v", err)
	}
	var rawRows, rollupBuckets int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM agent_telemetry WHERE agent_id=$1`, agentID).Scan(&rawRows); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(DISTINCT bucket_start) FROM telemetry_metric_rollups WHERE agent_id=$1 AND metric_name='cpu_used_percent'`, agentID).Scan(&rollupBuckets); err != nil {
		t.Fatal(err)
	}
	if rawRows != 3 || rollupBuckets != 2 {
		t.Fatalf("retention fixture state raw=%d rollup buckets=%d, want 3 and 2", rawRows, rollupBuckets)
	}
	mux := http.NewServeMux()
	NewHandler(pool, service).Register(mux)
	r := httptest.NewRequest(http.MethodGet, "/api/inventory/devices/"+deviceID+"/history?range=30d&metric=cpu_used_percent", nil)
	r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session.Token})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("history status=%d body=%s", w.Code, w.Body)
	}
	var history History
	if err = json.Unmarshal(w.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	if len(history.Series) != 1 || len(history.Series[0].Points) < 3 {
		t.Fatalf("history should expose two retained buckets and recent raw points, got %+v", history.Series)
	}
}

func inventoryTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("VIGIRA_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set VIGIRA_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	if _, err = admin.Exec(ctx, `BEGIN; SELECT pg_advisory_xact_lock(138139136); CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public; COMMIT;`); err != nil {
		t.Fatal(err)
	}
	schema := pgx.Identifier{fmt.Sprintf("inventoryview_test_%d", time.Now().UnixNano())}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, name := range []string{"000001_initial_schema.up.sql", "000002_local_auth.up.sql", "000003_agent_enrollment.up.sql", "000004_inventory.up.sql", "000005_telemetry.up.sql", "000006_telemetry_retention.up.sql"} {
		migration, readErr := os.ReadFile(filepath.Join("..", "..", "migrations", name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, err = pool.Exec(ctx, string(migration)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	return pool
}
