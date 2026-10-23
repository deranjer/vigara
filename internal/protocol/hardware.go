package protocol

import (
	"fmt"
	"time"
)

// HardwareInventory is best-effort static inventory. Missing values are unknown,
// not zero measurements. Each category reports collected, unavailable or unsupported.
type HardwareInventory struct {
	ObservedAt     time.Time           `json:"observedAt"`
	GPUStatus      string              `json:"gpuStatus"`
	GPUs           []HardwareComponent `json:"gpus,omitempty"`
	MemoryStatus   string              `json:"memoryStatus"`
	MemoryModules  []HardwareComponent `json:"memoryModules,omitempty"`
	BoardStatus    string              `json:"boardStatus"`
	Motherboard    *HardwareComponent  `json:"motherboard,omitempty"`
	FirmwareStatus string              `json:"firmwareStatus"`
	Firmware       *HardwareComponent  `json:"firmware,omitempty"`
}

type HardwareComponent struct {
	Name          string `json:"name,omitempty"`
	Manufacturer  string `json:"manufacturer,omitempty"`
	Model         string `json:"model,omitempty"`
	Version       string `json:"version,omitempty"`
	Slot          string `json:"slot,omitempty"`
	CapacityBytes uint64 `json:"capacityBytes,omitempty"`
	SpeedMTs      uint64 `json:"speedMTs,omitempty"`
}

// Validate bounds inventory before it is persisted with a host snapshot.
func (h *HardwareInventory) Validate(now time.Time) error {
	if h == nil {
		return nil
	}
	if h.ObservedAt.IsZero() || h.ObservedAt.After(now.Add(5*time.Minute)) || len(h.GPUs) > 16 || len(h.MemoryModules) > 64 {
		return fmt.Errorf("invalid hardware inventory bounds")
	}
	for _, status := range []string{h.GPUStatus, h.MemoryStatus, h.BoardStatus, h.FirmwareStatus} {
		if status != "collected" && status != "unavailable" && status != "unsupported" {
			return fmt.Errorf("invalid hardware inventory status")
		}
	}
	components := append(append([]HardwareComponent{}, h.GPUs...), h.MemoryModules...)
	if h.Motherboard != nil {
		components = append(components, *h.Motherboard)
	}
	if h.Firmware != nil {
		components = append(components, *h.Firmware)
	}
	for _, c := range components {
		for _, value := range []string{c.Name, c.Manufacturer, c.Model, c.Version, c.Slot} {
			if len(value) > 256 {
				return fmt.Errorf("hardware inventory field too long")
			}
		}
	}
	return nil
}
