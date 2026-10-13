//go:build linux

package collector

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/deranjer/vigara/internal/protocol"
)

func collectPlatformHardware(h *protocol.HardwareInventory, fail func(string, error)) {
	read := func(path string) string {
		file, err := os.Open(path)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				fail("hardware_platform", errors.New("one or more hardware details could not be read"))
			}
			return ""
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, 1025))
		if err != nil || len(data) > 1024 {
			fail("hardware_platform", errors.New("one or more hardware details could not be read"))
			return ""
		}
		return cleanHardwareText(strings.TrimSpace(string(data)), 256)
	}

	board := read("/sys/devices/virtual/dmi/id/board_vendor")
	model := read("/sys/devices/virtual/dmi/id/board_name")
	firmwareVendor := read("/sys/devices/virtual/dmi/id/bios_vendor")
	firmwareVersion := read("/sys/devices/virtual/dmi/id/bios_version")
	date := read("/sys/devices/virtual/dmi/id/bios_date")
	if board != "" || model != "" || firmwareVendor != "" || firmwareVersion != "" {
		h.Motherboard = protocol.Motherboard{Manufacturer: board, Model: model}
		h.Firmware = protocol.Firmware{Manufacturer: firmwareVendor, Version: firmwareVersion, ReleaseDate: date}
		h.BoardStatus = "available"
		if board == "" || model == "" || firmwareVendor == "" || firmwareVersion == "" || date == "" {
			h.BoardStatus = "partial"
		}
	} else {
		h.BoardStatus = "unavailable"
	}

	cards := mustGlob("/sys/class/drm/card[0-9]*")
	if len(cards) > 64 {
		cards = cards[:64]
	}
	gpuPartial := false
	for _, card := range cards {
		base := filepath.Join(card, "device")
		class := read(filepath.Join(base, "class"))
		if class != "0x030000" && class != "0x030200" {
			continue
		}
		vendorID := strings.TrimPrefix(read(filepath.Join(base, "vendor")), "0x")
		deviceID := strings.TrimPrefix(read(filepath.Join(base, "device")), "0x")
		vendor := map[string]string{"10de": "NVIDIA", "1002": "AMD", "8086": "Intel"}[strings.ToLower(vendorID)]
		if vendor == "" && vendorID != "" {
			vendor = "PCI vendor " + vendorID
		}
		g := protocol.HardwareGPU{Vendor: vendor}
		if deviceID != "" {
			g.Model = "PCI device " + strings.ToLower(vendorID) + ":" + strings.ToLower(deviceID)
		}
		if raw := read(filepath.Join(base, "mem_info_vram_total")); raw != "" {
			g.VRAMBytes, _ = strconv.ParseUint(raw, 10, 64)
		}
		if g.Vendor == "" || g.Model == "" || g.VRAMBytes == 0 {
			gpuPartial = true
		}
		h.GPUs = append(h.GPUs, g)
		if len(h.GPUs) == 8 {
			break
		}
	}
	if len(h.GPUs) > 0 {
		h.GPUStatus = "available"
		if gpuPartial {
			h.GPUStatus = "partial"
		}
	} else {
		h.GPUStatus = "unavailable"
	}
	// Linux exposes module details inconsistently; the agent does not invoke
	// privileged tools such as dmidecode to fill them in.
	h.MemoryStatus = "unsupported"

	blocks := mustGlob("/sys/block/*")
	if len(blocks) > 128 {
		blocks = blocks[:128]
	}
	diskPartial := false
	for _, entry := range blocks {
		name := filepath.Base(entry)
		if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") || strings.HasPrefix(name, "zram") {
			continue
		}
		model := read(filepath.Join(entry, "device", "model"))
		if model == "" {
			model = read(filepath.Join(entry, "device", "name"))
		}
		sectors, _ := strconv.ParseUint(read(filepath.Join(entry, "size")), 10, 64)
		if model == "" && sectors == 0 {
			continue
		}
		diskType := ""
		if rotational := read(filepath.Join(entry, "queue", "rotational")); rotational == "0" {
			diskType = "SSD"
		} else if rotational == "1" {
			diskType = "HDD"
		}
		capacity := uint64(0)
		if sectors <= ^uint64(0)/512 {
			capacity = sectors * 512
		}
		h.Disks = append(h.Disks, protocol.HardwareDisk{Model: model, CapacityBytes: capacity, Type: diskType})
		if model == "" || capacity == 0 || diskType == "" {
			diskPartial = true
		}
		if len(h.Disks) == 32 {
			break
		}
	}
	if len(h.Disks) > 0 {
		h.DiskStatus = "available"
		if diskPartial {
			h.DiskStatus = "partial"
		}
	} else {
		h.DiskStatus = "unavailable"
	}
}

func mustGlob(pattern string) []string { paths, _ := filepath.Glob(pattern); return paths }
