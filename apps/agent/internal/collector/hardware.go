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

// SMBIOS strings are reported metadata, not proof of the hosting platform.
// Do not infer Proxmox from generic QEMU, SeaBIOS or virtual graphics devices.
func hardwareEnvironment(manufacturer, model string) *protocol.HardwareEnvironment {
	vendor, product := strings.ToLower(manufacturer), strings.ToLower(model)
	hypervisor, platform := "", ""
	switch {
	case vendor == "proxmox" || vendor == "proxmox server solutions gmbh" || product == "proxmox":
		hypervisor, platform = "QEMU/KVM", "Proxmox (reported by SMBIOS)"
	case vendor == "qemu" || vendor == "kvm" || strings.HasPrefix(product, "kvm ") || product == "kvm":
		hypervisor = "QEMU/KVM"
	case strings.HasPrefix(vendor, "vmware") || product == "vmware virtual platform":
		hypervisor = "VMware"
	case product == "virtualbox":
		hypervisor = "VirtualBox"
	case vendor == "microsoft corporation" && product == "virtual machine":
		hypervisor = "Hyper-V"
	case vendor == "xen" || product == "hvm domu":
		hypervisor = "Xen"
	default:
		return nil
	}
	return &protocol.HardwareEnvironment{Kind: "virtual-machine", Hypervisor: hypervisor, Platform: platform}
}
