//go:build windows

package collector

import (
	"context"
	"encoding/json"
	"github.com/deranjer/vigara/internal/protocol"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// Query categories independently so a missing CIM provider does not erase others.
// AdapterRAM is intentionally excluded: Win32_VideoController exposes a uint32
// value which cannot represent modern GPUs' VRAM reliably.
const hardwareScript = `$ErrorActionPreference='Stop'
[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)
$r=@{gpuStatus='unavailable';memoryStatus='unavailable';boardStatus='unavailable';firmwareStatus='unavailable'}
try {$r.gpus=@(Get-CimInstance Win32_VideoController -OperationTimeoutSec 2 | Select-Object -First 16 | ForEach-Object {@{name=$_.Name;manufacturer=$_.AdapterCompatibility;version=$_.DriverVersion}}); if($r.gpus.Count){$r.gpuStatus='collected'}} catch {}
try {$r.memoryModules=@(Get-CimInstance Win32_PhysicalMemory -OperationTimeoutSec 2 | Select-Object -First 64 | ForEach-Object {@{slot=$_.DeviceLocator;manufacturer=$_.Manufacturer;model=$_.PartNumber;capacityBytes=[uint64]$_.Capacity;speedMTs=[uint64]$_.ConfiguredClockSpeed}}); if($r.memoryModules.Count){$r.memoryStatus='collected'}} catch {}
try {$b=Get-CimInstance Win32_BaseBoard -OperationTimeoutSec 2 | Select-Object -First 1; if($b){$r.motherboard=@{manufacturer=$b.Manufacturer;model=$b.Product;version=$b.Version};$r.boardStatus='collected'}} catch {}
try {$b=Get-CimInstance Win32_BIOS -OperationTimeoutSec 2 | Select-Object -First 1; if($b){$r.firmware=@{manufacturer=$b.Manufacturer;name=$b.Name;version=$b.SMBIOSBIOSVersion};$r.firmwareStatus='collected'}} catch {}
$r | ConvertTo-Json -Depth 4 -Compress`

func collectHardware(ctx context.Context) *protocol.HardwareInventory {
	result := emptyHardware()
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe"), "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", hardwareScript)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	output, err := command.Output()
	if err != nil || len(output) > 128*1024 {
		return result
	}
	var parsed protocol.HardwareInventory
	if json.Unmarshal(output, &parsed) != nil {
		return result
	}
	parsed.ObservedAt = result.ObservedAt
	for _, items := range [][]protocol.HardwareComponent{parsed.GPUs, parsed.MemoryModules} {
		for i := range items {
			cleanHardwareComponent(&items[i])
		}
	}
	if parsed.Motherboard != nil {
		cleanHardwareComponent(parsed.Motherboard)
	}
	if parsed.Firmware != nil {
		cleanHardwareComponent(parsed.Firmware)
	}
	return &parsed
}

func cleanHardwareComponent(c *protocol.HardwareComponent) {
	c.Name = hardwareText(c.Name)
	c.Manufacturer = hardwareText(c.Manufacturer)
	c.Model = hardwareText(c.Model)
	c.Version = hardwareText(c.Version)
	c.Slot = hardwareText(c.Slot)
}
