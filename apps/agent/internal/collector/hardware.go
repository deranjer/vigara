package collector

import (
	"github.com/deranjer/vigara/internal/protocol"
	"strings"
	"time"
)

func emptyHardware() *protocol.HardwareInventory {
	return &protocol.HardwareInventory{ObservedAt: time.Now().UTC(), GPUStatus: "unavailable", MemoryStatus: "unavailable", BoardStatus: "unavailable", FirmwareStatus: "unavailable"}
}

func hardwareText(value string) string {
	value = strings.TrimSpace(value)
	switch strings.ToLower(value) {
	case "unknown", "none", "not specified", "default string", "to be filled by o.e.m.":
		return ""
	}
	return truncate(value, 256)
}
