//go:build windows

package runtime

import (
	"github.com/deranjer/vigara/internal/protocol"
	"golang.org/x/sys/windows/registry"
)

func platformIdentifier() (protocol.DeviceIdentifier, bool) {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Cryptography`, registry.QUERY_VALUE)
	if err != nil {
		return protocol.DeviceIdentifier{}, false
	}
	defer key.Close()
	value, _, err := key.GetStringValue("MachineGuid")
	if err != nil {
		return protocol.DeviceIdentifier{}, false
	}
	digest, ok := stableMachineID(value)
	if !ok {
		return protocol.DeviceIdentifier{}, false
	}
	return protocol.DeviceIdentifier{Kind: "machine-id", Value: digest}, true
}
