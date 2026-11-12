package alerts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/apps/api/internal/db"
)

func TestInboxAcknowledgeMuteNotesAndRules(t *testing.T) {
	pool := alertsTestPool(t)
	ctx := context.Background()
	service := auth.NewService(pool, db.New(pool))
	session, err := service.CreateFirstUser(ctx, "inbox-admin", "Inbox Admin", "correct horse battery staple", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewHandler(pool, service).Register(mux)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session.Token})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	var dev string
	if err = pool.QueryRow(ctx, `INSERT INTO devices(display_name) VALUES('inbox-test') RETURNING id::text`).Scan(&dev); err != nil {
		t.Fatal(err)
	}
	insertRule(t, pool, "disk", "threshold", Config{DeviceID: dev, Metric: "cpu_used_percent", Operator: "gte", Threshold: 90})
	var rule, incident string
	if err = pool.QueryRow(ctx, `SELECT id::text FROM alert_rules WHERE name='disk'`).Scan(&rule); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `INSERT INTO alert_incidents(rule_id,dimension,status,summary,started_at) VALUES($1,$2,'firing','cpu high',now()) RETURNING id::text`, rule, dev+"/{}").Scan(&incident); err != nil {
		t.Fatal(err)
	}
	base := "/api/alerts/incidents/" + incident
	if w := request(http.MethodPost, base+"/acknowledge", ""); w.Code != http.StatusNoContent {
		t.Fatalf("acknowledge status=%d body=%s", w.Code, w.Body.String())
	}
	if w := request(http.MethodPut, base+"/mute", `{"minutes":0}`); w.Code != http.StatusBadRequest {
		t.Fatalf("zero mute status=%d", w.Code)
	}
	if w := request(http.MethodPut, base+"/mute", `{"minutes":60}`); w.Code != http.StatusNoContent {
		t.Fatalf("mute status=%d body=%s", w.Code, w.Body.String())
	}
	if w := request(http.MethodPost, base+"/notes", `{"body":"  raised the limit  "}`); w.Code != http.StatusCreated {
		t.Fatalf("note status=%d body=%s", w.Code, w.Body.String())
	}
	if w := request(http.MethodPost, "/api/alerts/incidents/00000000-0000-0000-0000-000000000000/notes", `{"body":"x"}`); w.Code != http.StatusNotFound {
		t.Fatalf("note on missing incident status=%d", w.Code)
	}
	w := request(http.MethodGet, "/api/alerts/incidents", "")
	var incidents []Incident
	if err = json.Unmarshal(w.Body.Bytes(), &incidents); err != nil || len(incidents) != 1 {
		t.Fatalf("incidents=%s err=%v", w.Body.String(), err)
	}
	got := incidents[0]
	if got.AcknowledgedAt == nil || got.AcknowledgedBy != "Inbox Admin" || got.MutedUntil == nil || time.Until(*got.MutedUntil) < 59*time.Minute || len(got.Notes) != 1 || got.Notes[0].Body != "raised the limit" || got.Notes[0].Author != "Inbox Admin" || got.RuleName != "disk" || got.Severity != "warning" {
		t.Fatalf("incident=%+v", got)
	}

	// An expired mute reads as unmuted, and a refire clears the acknowledgement.
	if _, err = pool.Exec(ctx, `UPDATE alert_incidents SET muted_until=now()-interval '1 minute',status='resolved',resolved_at=now() WHERE id=$1`, incident); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	observed := time.Now().UTC().Add(time.Second)
	if err = transition(ctx, tx, Rule{ID: rule, Name: "disk", Kind: "threshold"}, dev+"/{}", true, false, "cpu high again", &observed, time.Now().UTC(), false); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	w = request(http.MethodGet, "/api/alerts/incidents", "")
	incidents = nil
	if err = json.Unmarshal(w.Body.Bytes(), &incidents); err != nil || incidents[0].Status != "firing" || incidents[0].AcknowledgedAt != nil || incidents[0].MutedUntil != nil {
		t.Fatalf("refired incident=%+v err=%v", incidents, err)
	}
	if w = request(http.MethodDelete, base+"/acknowledge", ""); w.Code != http.StatusNoContent {
		t.Fatalf("unacknowledge status=%d", w.Code)
	}

	// Rules: severity and threshold can change; built-ins take only name and severity.
	if w = request(http.MethodPut, "/api/alerts/rules/"+rule, `{"name":"CPU","severity":"critical","config":{"deviceId":"`+dev+`","metric":"cpu_used_percent","operator":"gte","threshold":95}}`); w.Code != http.StatusOK {
		t.Fatalf("update rule status=%d body=%s", w.Code, w.Body.String())
	}
	assertCount(t, pool, `SELECT count(*) FROM alert_rules WHERE name='CPU' AND severity='critical' AND (config->>'threshold')::float=95`, 1)
	if w = request(http.MethodPut, "/api/alerts/rules/"+rule, `{"name":"CPU","severity":"loud"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad severity status=%d", w.Code)
	}
	pinned := "/api/alerts/rules/00000000-0000-0000-0000-000000000025"
	if w = request(http.MethodPut, pinned, `{"name":"Pinned","severity":"warning","config":{}}`); w.Code != http.StatusBadRequest {
		t.Fatalf("built-in config status=%d", w.Code)
	}
	if w = request(http.MethodPut, pinned, `{"name":"Pinned image moved","severity":"critical"}`); w.Code != http.StatusOK {
		t.Fatalf("built-in rename status=%d body=%s", w.Code, w.Body.String())
	}
	if w = request(http.MethodPost, "/api/alerts/rules/"+rule+"/disable", ""); w.Code != http.StatusNoContent {
		t.Fatalf("disable status=%d", w.Code)
	}
	if w = request(http.MethodPost, "/api/alerts/rules/"+rule+"/enable", ""); w.Code != http.StatusNoContent {
		t.Fatalf("enable status=%d", w.Code)
	}
	assertCount(t, pool, `SELECT count(*) FROM alert_rules WHERE name='CPU' AND enabled`, 1)

	// Maintenance across devices, and the timeline.
	if _, err = pool.Exec(ctx, `INSERT INTO device_maintenance_windows(device_id,starts_at,ends_at) VALUES($1,now()-interval '1 minute',now()+interval '1 hour')`, dev); err != nil {
		t.Fatal(err)
	}
	w = request(http.MethodGet, "/api/alerts/maintenance", "")
	var windows []FleetMaintenanceWindow
	if err = json.Unmarshal(w.Body.Bytes(), &windows); err != nil || len(windows) != 1 || !windows[0].Active || windows[0].DeviceName != "inbox-test" {
		t.Fatalf("maintenance=%s err=%v", w.Body.String(), err)
	}
	w = request(http.MethodGet, "/api/alerts/timeline", "")
	var items []TimelineItem
	if err = json.Unmarshal(w.Body.Bytes(), &items); err != nil || len(items) == 0 || items[0].Kind != "resolved" && items[0].Kind != "firing" {
		t.Fatalf("timeline=%s err=%v", w.Body.String(), err)
	}
}
