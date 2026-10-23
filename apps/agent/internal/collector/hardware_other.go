//go:build !windows && !linux

package collector

import (
	"context"
	"github.com/deranjer/vigara/internal/protocol"
)

func collectHardware(context.Context) *protocol.HardwareInventory {
	r := emptyHardware()
	r.GPUStatus = "unsupported"
	r.MemoryStatus = "unsupported"
	r.BoardStatus = "unsupported"
	r.FirmwareStatus = "unsupported"
	return r
}
