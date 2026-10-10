//go:build !linux && !windows

package runtime

import "github.com/deranjer/vigara/internal/protocol"

func platformIdentifier() (protocol.DeviceIdentifier, bool) {
	return protocol.DeviceIdentifier{}, false
}
