//go:build linux

package runtime

import (
	"os"

	"github.com/deranjer/vigara/internal/protocol"
)

func platformIdentifier() (protocol.DeviceIdentifier, bool) {
	data, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		return protocol.DeviceIdentifier{}, false
	}
	digest, ok := stableMachineID(string(data))
	if !ok {
		return protocol.DeviceIdentifier{}, false
	}
	return protocol.DeviceIdentifier{Kind: "machine-id", Value: digest}, true
}
