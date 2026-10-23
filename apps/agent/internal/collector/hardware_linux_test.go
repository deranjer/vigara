//go:build linux

package collector

import "testing"

func TestParseMemoryModulesSkipsEmptySlots(t *testing.T) {
	modules := parseMemoryModules(`Memory Device
    Size: No Module Installed
    Locator: DIMM0
Memory Device
    Size: 16 GB
    Locator: DIMM1
    Manufacturer: Example
    Part Number: RAM-16
    Configured Memory Speed: 3200 MT/s
Memory Device
    Size: Unknown
`)
	if len(modules) != 1 || modules[0].CapacityBytes != 16*1024*1024*1024 || modules[0].SpeedMTs != 3200 || modules[0].Slot != "DIMM1" {
		t.Fatalf("unexpected DIMMs: %+v", modules)
	}
}
