package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/deranjer/vigara/internal/protocol"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPollPersistsBoundedTelemetryQueueWithoutClearingUploadOutcome(t *testing.T) {
	pool, sessions := agentTestService(t)
	ctx := context.Background()
	device, err := createTestBackupAgent(ctx, t, pool)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewHandler(pool, sessions).Register(mux)
	if _, err = pool.Exec(ctx, `INSERT INTO agent_telemetry_delivery(agent_id,last_attempt_at,last_status_code,retry_at) VALUES($1,now(),429,now()+interval '1 minute')`, device.agentID); err != nil {
		t.Fatal(err)
	}
	older := time.Now().Add(-time.Hour)
	newer := time.Now().Add(-time.Minute)
	post := func(status *protocol.TelemetryQueueStatus, credential string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(protocol.AgentRequest{ProtocolVersion: protocol.CurrentVersion, AgentID: device.agentID, AgentVersion: "test", Platform: "linux", Architecture: "amd64", Capabilities: []string{"restic.backup"}, TelemetryDelivery: status})
		req := httptest.NewRequest("POST", "/api/agent/poll", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+credential)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	q := &protocol.TelemetryQueueStatus{QueuedSnapshots: 51, OldestQueuedAt: &older, NewestQueuedAt: &newer}
	w := post(q, device.credential)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var response protocol.AgentResponse
	if err = json.Unmarshal(w.Body.Bytes(), &response); err != nil || !response.SupportsTelemetryDelivery {
		t.Fatal("queue reporting not negotiated", err)
	}
	var count, code int
	var reported time.Time
	read := func() {
		t.Helper()
		if err := pool.QueryRow(ctx, "SELECT queued_snapshots,last_status_code,queue_reported_at FROM agent_telemetry_delivery WHERE agent_id=$1", device.agentID).Scan(&count, &code, &reported); err != nil {
			t.Fatal(err)
		}
	}
	read()
	if count != 51 || code != 429 || time.Since(reported) > time.Minute {
		t.Fatalf("queue=%d code=%d reported=%v", count, code, reported)
	}
	if w = post(nil, device.credential); w.Code != 200 {
		t.Fatal(w.Code)
	}
	read()
	if count != 51 {
		t.Fatal("old agent erased known count")
	}
	if w = post(&protocol.TelemetryQueueStatus{QueuedSnapshots: 101}, device.credential); w.Code != 400 {
		t.Fatal("invalid queue accepted", w.Code)
	}
	if w = post(&protocol.TelemetryQueueStatus{}, "wrong"); w.Code != 401 {
		t.Fatal("unauthenticated queue accepted", w.Code)
	}
	read()
	if count != 51 {
		t.Fatal("invalid report changed queue")
	}
	if w = post(&protocol.TelemetryQueueStatus{}, device.credential); w.Code != 200 {
		t.Fatal(w.Code)
	}
	read()
	if count != 0 || code != 429 {
		t.Fatal("empty queue did not update independently")
	}
}
