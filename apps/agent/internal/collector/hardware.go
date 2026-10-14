package collector

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/deranjer/vigara/internal/protocol"
	"github.com/shirou/gopsutil/v4/cpu"
)

func collectHardware(ctx context.Context, observedAt time.Time, fail func(string, error)) protocol.HardwareInventory {
	h := protocol.HardwareInventory{ObservedAt: observedAt, CPUStatus: "unavailable", GPUStatus: "unsupported", MemoryStatus: "unsupported", BoardStatus: "unsupported", DiskStatus: "unsupported", GPUs: []protocol.HardwareGPU{}, MemoryModules: []protocol.MemoryModule{}, Disks: []protocol.HardwareDisk{}}
	infos, err := cpu.InfoWithContext(ctx)
	if err != nil {
		fail("hardware_cpu", err)
	} else if len(infos) == 0 {
		fail("hardware_cpu", fmt.Errorf("no processor inventory returned"))
	} else {
		status := "available"
		if len(infos) > 8192 {
			infos = infos[:8192]
			status = "partial"
		}
		packages, cores := map[string]bool{}, map[string]bool{}
		for _, info := range infos {
			pkg := info.PhysicalID
			if pkg == "" {
				pkg = "0"
			}
			packages[pkg] = true
			core := info.CoreID
			if core == "" {
				core = fmt.Sprint(info.CPU)
			}
			cores[pkg+":"+core] = true
			if h.CPU.Vendor == "" {
				h.CPU.Vendor = cleanHardwareText(info.VendorID, 128)
			}
			if h.CPU.Model == "" {
				h.CPU.Model = cleanHardwareText(info.ModelName, 256)
			}
		}
		h.CPU.PhysicalPackages, h.CPU.PhysicalCores, h.CPU.LogicalThreads = len(packages), len(cores), len(infos)
		if h.CPU.PhysicalCores == 0 {
			for _, info := range infos {
				h.CPU.PhysicalCores += int(info.Cores)
			}
		}
		if h.CPU.Vendor == "" || h.CPU.Model == "" || h.CPU.PhysicalCores == 0 {
			status = "partial"
		}
		h.CPUStatus = status
	}
	collectPlatformHardware(&h, fail)
	return h
}

func cleanHardwareText(value string, max int) string {
	value = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value))
	if len(value) <= max {
		return value
	}
	value = value[:max]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return strings.TrimSpace(value)
}
