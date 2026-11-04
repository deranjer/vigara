package runtime

import (
	"context"
	"encoding/json"
	"github.com/deranjer/vigara/internal/protocol"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestTelemetryQueueReportingNegotiationAndRecovery(t *testing.T) {
	for _, supports := range []bool{false, true} {
		t.Run(map[bool]string{false: "old-server", true: "new-server"}[supports], func(t *testing.T) {
			var reports []*protocol.TelemetryQueueStatus
			reject := true
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/agent/telemetry" {
					if reject {
						w.WriteHeader(429)
					} else {
						w.WriteHeader(202)
					}
					return
				}
				var req protocol.AgentRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				reports = append(reports, req.TelemetryDelivery)
				_ = json.NewEncoder(w).Encode(protocol.AgentResponse{ProtocolVersion: protocol.CurrentVersion, PollAfterSeconds: 30, SupportsTelemetryDelivery: supports})
			}))
			defer server.Close()
			c, err := NewClient(filepath.Join(t.TempDir(), "agent.json"))
			if err != nil {
				t.Fatal(err)
			}
			c.State = State{ServerURL: server.URL, AgentID: "agent", Credential: "secret"}
			c.HTTP.Transport = server.Client().Transport
			oldest := time.Now().Add(-time.Hour).UTC()
			c.State.Telemetry = []protocol.TelemetrySnapshot{{ID: "old", ObservedAt: oldest}}
			_, _ = c.Cycle(context.Background())
			_, _ = c.Cycle(context.Background())
			if reports[0] != nil {
				t.Fatal("sent unknown field before negotiation")
			}
			if supports {
				if reports[1] == nil || reports[1].QueuedSnapshots != 3 || !reports[1].OldestQueuedAt.Equal(oldest) {
					t.Fatalf("wrong retained queue metadata: %+v", reports[1])
				}
			} else if reports[1] != nil {
				t.Fatal("old server received unsupported field")
			}
			reject = false
			if _, err = c.Cycle(context.Background()); err != nil {
				t.Fatal(err)
			}
			if supports && (reports[2] == nil || reports[2].QueuedSnapshots != 0 || reports[2].OldestQueuedAt != nil) {
				t.Fatalf("recovered queue not cleared: %+v", reports[2])
			}
		})
	}
}
