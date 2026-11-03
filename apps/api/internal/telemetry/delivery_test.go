package telemetry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/deranjer/vigara/internal/protocol"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDeliveryTracksRateLimitAndClearsOnAcceptedUpload(t *testing.T) {
	pool := telemetryTestPool(t)
	ctx := context.Background()
	var device, agent string
	if err := pool.QueryRow(ctx, "INSERT INTO devices(display_name) VALUES('delivery') RETURNING id::text").Scan(&device); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("credential"))
	if err := pool.QueryRow(ctx, `INSERT INTO agent_identities(device_id,credential_hash,protocol_version) VALUES($1,$2,'v1') RETURNING id::text`, device, sum[:]).Scan(&agent); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewHandler(pool).Register(mux)
	now := time.Now().UTC()
	post := func(id string, n int, credential string) *httptest.ResponseRecorder {
		batch := protocol.TelemetrySnapshotBatch{ProtocolVersion: protocol.CurrentVersion, AgentID: agent}
		for i := 0; i < n; i++ {
			batch.Snapshots = append(batch.Snapshots, validSnapshot(agent, fmt.Sprintf("%s-%d", id, i), now))
		}
		raw, _ := json.Marshal(batch)
		r := httptest.NewRequest("POST", "/api/agent/telemetry", bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+credential)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	if w := post("first", 10, "credential"); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := post("limited", 1, "credential"); w.Code != 429 || w.Header().Get("Retry-After") != "60" {
		t.Fatal(w.Code, w.Body.String())
	}
	var code int
	var retry, accepted *time.Time
	if err := pool.QueryRow(ctx, "SELECT last_status_code,retry_at,last_accepted_at FROM agent_telemetry_delivery WHERE agent_id=$1", agent).Scan(&code, &retry, &accepted); err != nil {
		t.Fatal(err)
	}
	if code != 429 || retry == nil || accepted == nil || !retry.After(now) {
		t.Fatal("missing rate limit or accepted status", code, retry, accepted)
	}
	if w := post("bad", 1, "wrong"); w.Code != http.StatusUnauthorized {
		t.Fatal(w.Code)
	}
	// Advance the limiter's window without sleeping.
	if _, err := pool.Exec(ctx, `UPDATE telemetry_ingest_limits SET window_started_at=now()-interval '2 minutes' WHERE agent_id=$1`, agent); err != nil {
		t.Fatal(err)
	}
	if w := post("recovered", 1, "credential"); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	if err := pool.QueryRow(ctx, "SELECT last_status_code,retry_at,last_accepted_at FROM agent_telemetry_delivery WHERE agent_id=$1", agent).Scan(&code, &retry, &accepted); err != nil {
		t.Fatal(err)
	}
	if code != 202 || retry != nil || accepted == nil {
		t.Fatal("rate limit did not clear", code, retry, accepted)
	}
}
