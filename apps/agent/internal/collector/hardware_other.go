//go:build !linux

package collector

import "github.com/deranjer/vigara/internal/protocol"

func collectPlatformHardware(h *protocol.HardwareInventory, _ func(string, error)) {
	h.GPUStatus, h.MemoryStatus, h.BoardStatus, h.DiskStatus = "unsupported", "unsupported", "unsupported", "unsupported"
}
