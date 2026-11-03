package protocol

import (
	"strings"
	"testing"
	"time"
)

func TestHardwareInventoryBounds(t *testing.T) {
	now := time.Now()
	var absent *HardwareInventory
	if absent.Validate(now) != nil {
		t.Fatal("old agents must remain compatible")
	}
	h := HardwareInventory{ObservedAt: now, GPUStatus: "collected", MemoryStatus: "unavailable", BoardStatus: "unsupported", FirmwareStatus: "collected"}
	if err := h.Validate(now); err != nil {
		t.Fatal(err)
	}
	h.Environment = &HardwareEnvironment{Kind: "virtual-machine", Hypervisor: "QEMU/KVM"}
	h.System = &HardwareComponent{Manufacturer: "QEMU", Model: "Standard PC"}
	if err := h.Validate(now); err != nil {
		t.Fatal(err)
	}
	h.Environment.Kind = "physical"
	if h.Validate(now) == nil {
		t.Fatal("accepted unknown environment kind")
	}
	h.Environment.Kind = "virtual-machine"
	h.Environment.Platform = strings.Repeat("x", 257)
	if h.Validate(now) == nil {
		t.Fatal("accepted oversized environment field")
	}
	h.Environment.Platform = ""
	h.System.Model = strings.Repeat("x", 257)
	if h.Validate(now) == nil {
		t.Fatal("accepted oversized system model")
	}
	h.System.Model = "Standard PC"
	h.GPUs = []HardwareComponent{{Name: strings.Repeat("x", 257)}}
	if h.Validate(now) == nil {
		t.Fatal("accepted oversized field")
	}
	h.GPUs = make([]HardwareComponent, 17)
	if h.Validate(now) == nil {
		t.Fatal("accepted oversized GPU list")
	}
	h.GPUs = nil
	h.MemoryStatus = "success"
	if h.Validate(now) == nil {
		t.Fatal("accepted unknown status")
	}
}
