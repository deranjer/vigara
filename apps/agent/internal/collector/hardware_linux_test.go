//go:build linux

package collector

import "testing"

func TestDetectedVM(t *testing.T) {
	for _, value := range []string{"kvm\n", "qemu\n", "microsoft\n", "vmware\n"} {
		if got := detectedVM(value); got == nil || got.Kind != "virtual-machine" {
			t.Fatalf("missed VM %q: %+v", value, got)
		}
	}
	for _, value := range []string{"", "none\n", "docker\n", "lxc\n", "unknown-output\n"} {
		if got := detectedVM(value); got != nil {
			t.Fatalf("misidentified %q as VM: %+v", value, got)
		}
	}
}

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
