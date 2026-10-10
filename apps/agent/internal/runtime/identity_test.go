package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/deranjer/vigara/internal/protocol"
)

func TestStableMachineIDIsValidatedAndOneWay(t *testing.T) {
	raw := "00112233-4455-6677-8899-aabbccddeeff"
	first, ok := stableMachineID(raw)
	if !ok || len(first) != 64 {
		t.Fatal("valid machine ID did not produce a SHA-256 identifier")
	}
	second, ok := stableMachineID("{00112233445566778899AABBCCDDEEFF}")
	if !ok || first != second {
		t.Fatal("equivalent machine ID forms did not produce the same identifier")
	}
	if first == raw {
		t.Fatal("raw machine ID was returned")
	}
	for _, invalid := range []string{"", "unknown", "00000000000000000000000000000000", "ffffffffffffffffffffffffffffffff", "not-a-guid"} {
		if _, ok := stableMachineID(invalid); ok {
			t.Fatal("invalid machine ID was accepted")
		}
	}
}

func TestEnrollmentPayloadIncludesOnlyAvailableIdentifiers(t *testing.T) {
	capability := protocol.CapabilityReport{ProtocolVersion: protocol.CurrentVersion, Platform: "linux", Architecture: "amd64", AgentVersion: AgentVersion}
	identifier := protocol.DeviceIdentifier{Kind: "machine-id", Value: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"}
	withID := enrollmentPayload("token", "name", capability, []protocol.DeviceIdentifier{identifier})
	encoded, err := json.Marshal(withID)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err = json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	var got []protocol.DeviceIdentifier
	if err = json.Unmarshal(decoded["identifiers"], &got); err != nil || len(got) != 1 || got[0] != identifier {
		t.Fatal("enrollment payload did not include the stable identifier")
	}
	withoutID := enrollmentPayload("token", "name", capability, nil)
	if _, exists := withoutID["identifiers"]; exists {
		t.Fatal("enrollment payload included an unavailable identifier")
	}
}

func TestEnrollSendsAvailablePlatformIdentifier(t *testing.T) {
	var received struct {
		Identifiers []protocol.DeviceIdentifier `json:"identifiers"`
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode enrollment request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"agentId":"agent-1","credential":"credential-1"}`))
	}))
	defer server.Close()
	client, err := NewClient(filepath.Join(t.TempDir(), "state", "agent.json"))
	if err != nil {
		t.Fatal(err)
	}
	client.HTTP.Transport = server.Client().Transport
	if err = client.Enroll(context.Background(), server.URL, "one-time-enrollment-token", "Test host"); err != nil {
		t.Fatal(err)
	}
	want, ok := platformIdentifier()
	if ok && (len(received.Identifiers) != 1 || received.Identifiers[0] != want) {
		t.Fatal("enrollment did not send the available stable identifier")
	}
	if !ok && len(received.Identifiers) != 0 {
		t.Fatal("enrollment sent an unavailable stable identifier")
	}
}
