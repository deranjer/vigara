package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deranjer/vigara/internal/protocol"
)

func TestHTTPSRedirectCannotForwardAgentCredential(t *testing.T) {
	forwarded := false
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { forwarded = true; w.WriteHeader(http.StatusOK) }))
	defer target.Close()
	secretMarker := "private-server-response"
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL+"/capture")
		w.WriteHeader(http.StatusTemporaryRedirect)
		_, _ = w.Write([]byte(secretMarker))
	}))
	defer redirect.Close()
	c, err := NewClient(filepath.Join(t.TempDir(), "state", "agent.json"))
	if err != nil {
		t.Fatal(err)
	}
	c.State = State{ServerURL: redirect.URL, AgentID: "agent-1", Credential: "a-private-credential"}
	c.HTTP.Transport = redirect.Client().Transport
	_, err = c.Cycle(context.Background())
	if err == nil || !strings.Contains(err.Error(), "HTTP 307") {
		t.Fatalf("redirect response error = %v", err)
	}
	if strings.Contains(err.Error(), secretMarker) || strings.Contains(err.Error(), c.State.Credential) {
		t.Fatalf("response leaked secret material: %v", err)
	}
	if forwarded {
		t.Fatal("agent credential was forwarded to redirect target")
	}
}

func TestOfflineReportsRemainPersistedWhenServerRejectsBatch(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("credential must never be reflected"))
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "state", "agent.json")
	state := State{ServerURL: server.URL, AgentID: "agent", Credential: "secret", Pending: []protocol.JobResult{{ID: "stable-result-id", AgentID: "agent", ProtocolVersion: protocol.CurrentVersion, Status: protocol.JobFailed}}}
	if err := Save(path, state); err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(path)
	if err != nil {
		t.Fatal(err)
	}
	client.HTTP.Transport = server.Client().Transport
	if _, err := client.Cycle(context.Background()); err == nil || strings.Contains(err.Error(), "credential") {
		t.Fatalf("report failure error leaked server content: %v", err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Pending) != 1 || reloaded.Pending[0].ID != "stable-result-id" {
		t.Fatalf("offline report was lost after rejection: %+v", reloaded.Pending)
	}
	if len(reloaded.Telemetry) != 1 || reloaded.Telemetry[0].ID == "" {
		t.Fatalf("collected telemetry was not persisted before retry: %+v", reloaded.Telemetry)
	}
}

func TestTelemetrySnapshotIDsStayStableAcrossRejectedRetries(t *testing.T) {
	var received [][]string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var batch protocol.TelemetrySnapshotBatch
		if r.URL.Path == "/api/agent/telemetry" {
			if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
				t.Errorf("decode telemetry batch: %v", err)
			}
			ids := make([]string, len(batch.Snapshots))
			for i, snapshot := range batch.Snapshots {
				ids[i] = snapshot.ID
			}
			received = append(received, ids)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "state", "agent.json")
	client, err := NewClient(path)
	if err != nil {
		t.Fatal(err)
	}
	client.State = State{ServerURL: server.URL, AgentID: "agent", Credential: "secret"}
	client.HTTP.Transport = server.Client().Transport
	for i := 0; i < 2; i++ {
		if _, err := client.Cycle(context.Background()); err == nil {
			t.Fatal("expected server rejection")
		}
	}
	if len(received) != 2 || len(received[0]) == 0 || len(received[1]) < len(received[0]) {
		t.Fatalf("unexpected retry batches: %+v", received)
	}
	if received[0][0] != received[1][0] {
		t.Fatalf("snapshot retry ID changed: %q != %q", received[0][0], received[1][0])
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Telemetry) < 2 || reloaded.Telemetry[0].ID != received[0][0] {
		t.Fatalf("queue did not retain retry identity: %+v", reloaded.Telemetry)
	}
}

func TestRejectedTelemetryDoesNotBlockNormalPoll(t *testing.T) {
	pollCalled := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/agent/telemetry":
			http.Error(w, "snapshot rejected", http.StatusBadRequest)
		case "/api/agent/poll":
			pollCalled = true
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(protocol.AgentResponse{ProtocolVersion: protocol.CurrentVersion, PollAfterSeconds: 30})
		default:
			t.Errorf("unexpected agent request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "state", "agent.json")
	client, err := NewClient(path)
	if err != nil {
		t.Fatal(err)
	}
	client.State = State{ServerURL: server.URL, AgentID: "agent", Credential: "secret"}
	client.HTTP.Transport = server.Client().Transport
	delay, err := client.Cycle(context.Background())
	var deliveryErr *TelemetryDeliveryError
	if !errors.As(err, &deliveryErr) {
		t.Fatalf("Cycle error = %v, want surfaced nonfatal telemetry delivery error", err)
	}
	if !pollCalled {
		t.Fatal("telemetry rejection prevented normal server poll")
	}
	if delay < 30*time.Second {
		t.Fatalf("poll delay = %s, want normal server cadence", delay)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Telemetry) != 1 || reloaded.Telemetry[0].ID == "" {
		t.Fatalf("rejected snapshot was not retained: %+v", reloaded.Telemetry)
	}
}

func TestServerURLRequiresHTTPSOutsideLoopback(t *testing.T) {
	for _, raw := range []string{"http://vigira.example", "https://user:pass@vigira.example", "https://vigira.example/path?token=x"} {
		if err := validateServerURL(raw); err == nil {
			t.Errorf("accepted unsafe server URL %q", raw)
		}
	}
	for _, raw := range []string{"https://vigira.example", "http://127.0.0.1:8080", "http://localhost:8080"} {
		if err := validateServerURL(raw); err != nil {
			t.Errorf("rejected valid server URL %q: %v", raw, err)
		}
	}
}

func TestStateIsAtomicPrivateAndQueueBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "agent.json")
	state := State{ServerURL: "https://example.invalid", AgentID: "agent", Credential: "credential"}
	for i := 0; i < MaxQueuedReports; i++ {
		if err := state.Enqueue(protocol.JobResult{ID: fmt.Sprint(i), AgentID: "agent", Status: protocol.JobFailed}); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}
	for i := 0; i < MaxQueuedTelemetry; i++ {
		if err := state.EnqueueTelemetry(protocol.TelemetrySnapshot{ID: fmt.Sprint(i), AgentID: "agent"}); err != nil {
			t.Fatalf("enqueue telemetry %d: %v", i, err)
		}
	}
	if err := state.Enqueue(protocol.JobResult{ID: "overflow"}); err == nil {
		t.Fatal("queue accepted more than its configured bound")
	}
	if err := state.EnqueueTelemetry(protocol.TelemetrySnapshot{ID: "overflow"}); err == nil {
		t.Fatal("telemetry queue accepted more than its configured bound")
	}
	if err := Save(path, state); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.AgentID != state.AgentID || len(reloaded.Pending) != MaxQueuedReports || len(reloaded.Telemetry) != MaxQueuedTelemetry {
		t.Fatalf("reloaded state lost data: agent=%q pending=%d telemetry=%d", reloaded.AgentID, len(reloaded.Pending), len(reloaded.Telemetry))
	}
	assertPrivateStateFile(t, path)
}

func TestAgentResponseAcceptsAdditiveOptionalFields(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"protocolVersion":"v1","pollAfterSeconds":30,"futureOptionalField":{"enabled":true}}`))
	}))
	defer server.Close()
	client, err := NewClient(filepath.Join(t.TempDir(), "state", "agent.json"))
	if err != nil {
		t.Fatal(err)
	}
	client.State = State{ServerURL: server.URL, AgentID: "agent", Credential: "credential"}
	client.HTTP.Transport = server.Client().Transport
	delay, err := client.Cycle(context.Background())
	if err != nil {
		t.Fatalf("decode additive AgentResponse field: %v", err)
	}
	if delay < 30*time.Second {
		t.Fatalf("poll delay = %s, want at least 30 seconds", delay)
	}
}

func TestDockerTierCapabilitiesRequireLocalOptIn(t *testing.T) {
	c, err := NewClient(filepath.Join(t.TempDir(), "state", "agent.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		enabled, mode string
		want          []string
	}{
		{"", "deploy", nil},
		{"true", "", nil},
		{"true", "read", nil},
		{"true", "manage", []string{"docker.manage"}},
		{"true", "exec", []string{"docker.manage", "docker.exec"}},
		{"true", "deploy", []string{"docker.manage", "docker.exec", "docker.deploy"}},
		{"true", "bogus", nil},
	} {
		t.Setenv("VIGIRA_DOCKER_ENABLED", tc.enabled)
		t.Setenv("VIGIRA_DOCKER_MODE", tc.mode)
		got := c.capabilities()
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("enabled=%q mode=%q capabilities = %v, want %v", tc.enabled, tc.mode, got, tc.want)
		}
	}
}
