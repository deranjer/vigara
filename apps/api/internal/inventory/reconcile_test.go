package inventory

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/deranjer/vigara/internal/protocol"
)

func TestNormalizeUUIDRepresentations(t *testing.T) {
	canonical, err := normalize([]protocol.DeviceIdentifier{{Kind: "system-uuid", Value: "00112233-4455-6677-8899-AABBCCDDEEFF"}})
	if err != nil {
		t.Fatal(err)
	}
	braced, err := normalize([]protocol.DeviceIdentifier{{Kind: " SYSTEM-UUID ", Value: "{00112233445566778899aabbccddeeff}"}})
	if err != nil {
		t.Fatal(err)
	}
	if canonical[0].normalized != braced[0].normalized {
		t.Fatalf("UUID forms normalized differently: %q vs %q", canonical[0].normalized, braced[0].normalized)
	}
}

func TestRawAndHashedMachineIDsReconcileToSameKey(t *testing.T) {
	raw := "00112233445566778899aabbccddeeff"
	digest := sha256.Sum256([]byte("machine-id\x00" + raw))
	hashed := hex.EncodeToString(digest[:])
	fromAgent, err := normalize([]protocol.DeviceIdentifier{{Kind: "machine-id", Value: hashed}})
	if err != nil {
		t.Fatal(err)
	}
	fromDiscovery, err := normalize([]protocol.DeviceIdentifier{{Kind: "machine-id", Value: raw}})
	if err != nil {
		t.Fatal(err)
	}
	if fromAgent[0].normalized != fromDiscovery[0].normalized {
		t.Fatal("raw discovery and hashed agent IDs did not canonicalize equally")
	}
}

func TestRejectUnsafeDeviceIdentifiers(t *testing.T) {
	for _, identifier := range []protocol.DeviceIdentifier{
		{Kind: "machine-id", Value: "00000000000000000000000000000000"},
		{Kind: "system-uuid", Value: "To Be Filled By O.E.M."},
		{Kind: "system-uuid", Value: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"},
		{Kind: "bios-serial", Value: "SAME-SERIAL-ON-EVERY-VENDOR"},
		{Kind: "cloud-instance-id", Value: "i-123456"},
	} {
		if _, err := normalize([]protocol.DeviceIdentifier{identifier}); err == nil {
			t.Errorf("accepted unsafe key %#v", identifier)
		}
	}
}
