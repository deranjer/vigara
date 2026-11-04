//go:build linux

package collector

import (
	"context"
	"github.com/deranjer/vigara/internal/protocol"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func collectHardware(ctx context.Context) *protocol.HardwareInventory {
	r := emptyHardware()
	read := func(path string) string { data, _ := os.ReadFile(path); return hardwareText(string(data)) }
	system := &protocol.HardwareComponent{Manufacturer: read("/sys/class/dmi/id/sys_vendor"), Model: read("/sys/class/dmi/id/product_name")}
	if system.Manufacturer != "" || system.Model != "" {
		r.System = system
		r.Environment = hardwareEnvironment(system.Manufacturer, system.Model)
	}
	if r.Environment == nil {
		// --vm excludes containers; failure or missing tools leaves detection unknown.
		detectCtx, cancel := context.WithTimeout(ctx, time.Second)
		output, err := exec.CommandContext(detectCtx, "systemd-detect-virt", "--vm").Output()
		cancel()
		if err == nil {
			r.Environment = detectedVM(string(output))
		}
	}
	b := &protocol.HardwareComponent{Manufacturer: read("/sys/class/dmi/id/board_vendor"), Model: read("/sys/class/dmi/id/board_name"), Version: read("/sys/class/dmi/id/board_version")}
	if b.Manufacturer != "" || b.Model != "" {
		r.Motherboard = b
		r.BoardStatus = "collected"
	}
	f := &protocol.HardwareComponent{Manufacturer: read("/sys/class/dmi/id/bios_vendor"), Version: read("/sys/class/dmi/id/bios_version")}
	if f.Manufacturer != "" || f.Version != "" {
		r.Firmware = f
		r.FirmwareStatus = "collected"
	}
	devices, _ := filepath.Glob("/sys/bus/pci/devices/*")
	for _, path := range devices {
		if ctx.Err() != nil || len(r.GPUs) >= 16 {
			break
		}
		if !strings.HasPrefix(read(filepath.Join(path, "class")), "0x03") {
			continue
		}
		vendor := read(filepath.Join(path, "vendor"))
		device := read(filepath.Join(path, "device"))
		driver, _ := os.Readlink(filepath.Join(path, "driver"))
		name := "PCI " + vendor + ":" + device
		if driver != "" {
			name += " (" + filepath.Base(driver) + ")"
		}
		r.GPUs = append(r.GPUs, protocol.HardwareComponent{Name: name, Slot: filepath.Base(path)})
	}
	if len(r.GPUs) > 0 {
		r.GPUStatus = "collected"
	}
	// dmidecode may be missing or require root. Do not elevate or invent DIMMs.
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "dmidecode", "--type", "17").Output()
	if err == nil && len(output) <= 128*1024 {
		r.MemoryModules = parseMemoryModules(string(output))
		if len(r.MemoryModules) > 0 {
			r.MemoryStatus = "collected"
		}
	}
	return r
}

func detectedVM(output string) *protocol.HardwareEnvironment {
	value := strings.TrimSpace(output)
	// Only accept known VM results, never arbitrary command output or "none".
	names := map[string]string{"qemu": "QEMU/KVM", "kvm": "QEMU/KVM", "amazon": "Amazon EC2", "vmware": "VMware", "microsoft": "Hyper-V", "oracle": "VirtualBox", "xen": "Xen", "bochs": "Bochs", "parallels": "Parallels", "bhyve": "bhyve", "apple": "Apple Virtualization"}
	if name := names[value]; name != "" {
		return &protocol.HardwareEnvironment{Kind: "virtual-machine", Hypervisor: name}
	}
	return nil
}

func parseMemoryModules(output string) []protocol.HardwareComponent {
	var modules []protocol.HardwareComponent
	for _, block := range strings.Split(output, "Memory Device")[1:] {
		m := protocol.HardwareComponent{}
		for _, line := range strings.Split(block, "\n") {
			key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
			if !ok {
				continue
			}
			value = hardwareText(value)
			switch key {
			case "Locator":
				m.Slot = value
			case "Manufacturer":
				m.Manufacturer = value
			case "Part Number":
				m.Model = value
			case "Size":
				parts := strings.Fields(value)
				if len(parts) == 2 {
					n, _ := strconv.ParseUint(parts[0], 10, 40)
					switch parts[1] {
					case "MB":
						m.CapacityBytes = n * 1024 * 1024
					case "GB":
						m.CapacityBytes = n * 1024 * 1024 * 1024
					}
				}
			case "Configured Memory Speed":
				parts := strings.Fields(value)
				if len(parts) > 0 {
					m.SpeedMTs, _ = strconv.ParseUint(parts[0], 10, 32)
				}
			}
		}
		if m.CapacityBytes > 0 && len(modules) < 64 {
			modules = append(modules, m)
		}
	}
	return modules
}
