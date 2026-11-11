package inventoryview

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/apps/api/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

type trendFixture struct {
	t       *testing.T
	pool    *pgxpool.Pool
	handler *Handler
	mux     *http.ServeMux
	token   string
	device  string
	agent   string
	now     time.Time
}

func newTrendFixture(t *testing.T, latest string) *trendFixture {
	t.Helper()
	pool := inventoryTestPool(t)
	ctx := context.Background()
	service := auth.NewService(pool, db.New(pool))
	session, err := service.CreateFirstUser(ctx, "trend-admin", "Trend Admin", "correct horse battery", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	f := &trendFixture{t: t, pool: pool, token: session.Token, now: time.Now().UTC().Truncate(time.Hour).Add(30 * time.Minute)}
	if err = pool.QueryRow(ctx, `INSERT INTO devices(display_name) VALUES('trend device') RETURNING id::text`).Scan(&f.device); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `INSERT INTO agent_identities(device_id,credential_hash,protocol_version) VALUES($1,decode('aa11','hex'),'v1') RETURNING id::text`, f.device).Scan(&f.agent); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO agent_telemetry_latest(agent_id,report_id,observed_at,payload,received_at) VALUES($1,'latest',$2,$3::jsonb,$2)`, f.agent, f.now, latest); err != nil {
		t.Fatal(err)
	}
	f.handler = NewHandler(pool, service)
	f.handler.now = func() time.Time { return f.now }
	f.mux = http.NewServeMux()
	f.handler.Register(f.mux)
	return f
}

func (f *trendFixture) get(path string, cookie bool) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if cookie {
		r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: f.token})
	}
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}

func (f *trendFixture) trends(rangeName string) Trends {
	f.t.Helper()
	w := f.get(fmt.Sprintf("/api/inventory/devices/%s/trends?range=%s", f.device, rangeName), true)
	if w.Code != http.StatusOK {
		f.t.Fatalf("trends status=%d body=%s", w.Code, w.Body)
	}
	var out Trends
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		f.t.Fatal(err)
	}
	return out
}

func (f *trendFixture) raw(report string, observed time.Time, payload string, rolled bool) {
	f.t.Helper()
	var rolledAt any
	if rolled {
		rolledAt = observed
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO agent_telemetry(agent_id,report_id,observed_at,payload,rolled_at) VALUES($1,$2,$3,$4::jsonb,$5)`, f.agent, report, observed, payload, rolledAt); err != nil {
		f.t.Fatal(err)
	}
}

func cpuPayload(value float64) string {
	return fmt.Sprintf(`{"metrics":[{"name":"cpu_used_percent","unit":"%%","value":%v}]}`, value)
}

func (f *trendFixture) bin(trends Trends, at time.Time) int {
	return int(at.Sub(trends.WindowStart) / (time.Duration(trends.StepSeconds) * time.Second))
}

const trendLatest = `{"metrics":[
	{"name":"cpu_used_percent","unit":"%","value":5},
	{"name":"docker_memory_usage_bytes","unit":"By","value":1,"labels":{"container_id":"live"}}]}`

func TestTrendsCoverOnlyCurrentSeriesOnAFixedGrid(t *testing.T) {
	f := newTrendFixture(t, trendLatest)
	for i, minutes := range []int{10, 5, 1} {
		at := f.now.Add(-time.Duration(minutes) * time.Minute)
		f.raw(fmt.Sprintf("r%d", i), at, fmt.Sprintf(`{"metrics":[{"name":"cpu_used_percent","unit":"%%","value":%d},{"name":"docker_memory_usage_bytes","unit":"By","value":%d,"labels":{"container_id":"live"}},{"name":"docker_memory_usage_bytes","unit":"By","value":7,"labels":{"container_id":"recreated-away"}}]}`, (i+1)*10, (i+1)*1000), false)
	}
	trends := f.trends("1h")
	if trends.Bins != 60 || trends.StepSeconds != 60 || len(trends.Series) != 2 {
		t.Fatalf("bins=%d step=%d series=%d, want 60, 60 and only the two current series", trends.Bins, trends.StepSeconds, len(trends.Series))
	}
	if !trends.WindowEnd.After(f.now) || trends.WindowStart.Unix()%60 != 0 || trends.WindowEnd.Sub(trends.WindowStart) != time.Hour {
		t.Fatalf("grid is not aligned to the step: %v - %v", trends.WindowStart, trends.WindowEnd)
	}
	var cpu *TrendSeries
	for i := range trends.Series {
		if string(trends.Series[i].Labels) != "{}" && trends.Series[i].Name == "cpu_used_percent" {
			t.Fatalf("unlabelled series should report empty labels: %s", trends.Series[i].Labels)
		}
		if trends.Series[i].Name == "cpu_used_percent" {
			cpu = &trends.Series[i]
		}
	}
	if cpu == nil || len(cpu.Values) != 60 {
		t.Fatalf("missing cpu series: %+v", trends.Series)
	}
	want := map[time.Duration]float64{10 * time.Minute: 10, 5 * time.Minute: 20, time.Minute: 30}
	nonNull := 0
	for _, v := range cpu.Values {
		if v != nil {
			nonNull++
		}
	}
	if nonNull != 3 {
		t.Fatalf("expected three measured bins and gaps elsewhere, got %d", nonNull)
	}
	for ago, value := range want {
		v := cpu.Values[f.bin(trends, f.now.Add(-ago))]
		if v == nil || float64(*v) != value {
			t.Fatalf("value %v ago = %v, want %v", ago, v, value)
		}
	}
	for _, series := range trends.Series {
		if series.Name == "docker_memory_usage_bytes" && series.Unit != "By" {
			t.Fatalf("unit lost: %+v", series)
		}
	}
}

func TestTrendsAreAuthenticatedValidatedAndShareOneComputation(t *testing.T) {
	f := newTrendFixture(t, trendLatest)
	f.raw("r1", f.now.Add(-2*time.Minute), cpuPayload(40), false)
	if got := f.get("/api/inventory/devices/"+f.device+"/trends?range=1h", false).Code; got != http.StatusUnauthorized {
		t.Fatalf("anonymous status=%d", got)
	}
	for _, path := range []string{"/api/inventory/devices/" + f.device + "/trends", "/api/inventory/devices/" + f.device + "/trends?range=2h", "/api/inventory/devices/not-a-uuid/trends?range=1h"} {
		if got := f.get(path, true).Code; got != http.StatusBadRequest {
			t.Fatalf("%s status=%d, want 400", path, got)
		}
	}
	if got := f.get("/api/inventory/devices/00000000-0000-0000-0000-000000000000/trends?range=1h", true).Code; got != http.StatusNotFound {
		t.Fatalf("unknown device status=%d", got)
	}
	first := f.get("/api/inventory/devices/"+f.device+"/trends?range=1h", true).Body.String()
	f.raw("r2", f.now.Add(-time.Minute), cpuPayload(60), false)
	if again := f.get("/api/inventory/devices/"+f.device+"/trends?range=1h", true).Body.String(); again != first {
		t.Fatal("a second request inside the cache period must reuse the first result")
	}
	f.now = f.now.Add(time.Minute)
	if later := f.get("/api/inventory/devices/"+f.device+"/trends?range=1h", true).Body.String(); later == first {
		t.Fatal("a request after the cache period must see new data")
	}
}

func TestTrendsAndHistoryDoNotCountSummarisedSnapshotsTwice(t *testing.T) {
	f := newTrendFixture(t, trendLatest)
	hour := f.now.Truncate(time.Hour)
	earlier, summarised := hour.Add(-3*time.Hour), hour.Add(-2*time.Hour)
	ctx := context.Background()
	// An old hour that only exists as a summary (average 50 over 4 samples).
	if _, err := f.pool.Exec(ctx, `INSERT INTO telemetry_metric_rollups(agent_id,metric_name,bucket_start,sample_count,sum_value,min_value,max_value) VALUES($1,'cpu_used_percent',$2,4,200,20,80)`, f.agent, earlier); err != nil {
		t.Fatal(err)
	}
	// An hour whose raw samples (value 999) were already added to its summary (average 10).
	if _, err := f.pool.Exec(ctx, `INSERT INTO telemetry_metric_rollups(agent_id,metric_name,bucket_start,sample_count,sum_value,min_value,max_value) VALUES($1,'cpu_used_percent',$2,2,20,5,15)`, f.agent, summarised); err != nil {
		t.Fatal(err)
	}
	f.raw("rolled-1", summarised.Add(10*time.Minute), cpuPayload(999), true)
	f.raw("rolled-2", summarised.Add(20*time.Minute), cpuPayload(999), true)
	// The hour in progress has not been summarised yet and is still raw.
	f.raw("live-1", hour.Add(5*time.Minute), cpuPayload(100), false)
	f.raw("live-2", hour.Add(15*time.Minute), cpuPayload(200), false)
	if _, err := f.pool.Exec(ctx, `INSERT INTO telemetry_metric_series(agent_id,metric_name,labels) VALUES($1,'cpu_used_percent','{}')`, f.agent); err != nil {
		t.Fatal(err)
	}

	week := f.trends("7d")
	if week.Bins != 168 || week.StepSeconds != 3600 || len(week.Series) != 1 {
		t.Fatalf("week grid bins=%d step=%d series=%d", week.Bins, week.StepSeconds, len(week.Series))
	}
	for name, want := range map[string]struct {
		at    time.Time
		value float64
	}{"summary only": {earlier, 50}, "summary with rolled raw": {summarised, 10}, "unsummarised raw": {hour, 150}} {
		v := week.Series[0].Values[f.bin(week, want.at)]
		if v == nil || float64(*v) != want.value {
			t.Fatalf("%s: got %v, want %v (rolled raw samples must not be added to their own summary)", name, v, want.value)
		}
	}

	// The history endpoint (used by the comparison tab) must agree for hour-sized bins...
	var week7 History
	w := f.get(fmt.Sprintf("/api/inventory/devices/%s/history?range=7d&metric=cpu_used_percent", f.device), true)
	if w.Code != 200 {
		t.Fatalf("history 7d status=%d body=%s", w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &week7); err != nil {
		t.Fatal(err)
	}
	var samples int64
	for _, p := range week7.Series[0].Points {
		samples += p.Samples
		if *p.Max >= 999 {
			t.Fatalf("a summarised raw sample was counted again: %+v", p)
		}
	}
	if samples != 4+2+2 {
		t.Fatalf("7d history counted %d samples, want 8", samples)
	}
	// ...and for minute detail, where raw samples replace the summary of their own hour.
	var day History
	w = f.get(fmt.Sprintf("/api/inventory/devices/%s/history?range=24h&metric=cpu_used_percent", f.device), true)
	if w.Code != 200 {
		t.Fatalf("history 24h status=%d body=%s", w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &day); err != nil {
		t.Fatal(err)
	}
	samples = 0
	for _, p := range day.Series[0].Points {
		samples += p.Samples
	}
	if samples != 4+2+2 {
		t.Fatalf("24h history counted %d samples, want 8 (the summary of an hour with raw detail must be skipped)", samples)
	}
}
