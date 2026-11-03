package collector

import "testing"

func TestHardwareEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name, manufacturer, model, hypervisor, platform string
		vm                                              bool
	}{
		{"qemu is not necessarily proxmox", "QEMU", "Standard PC (Q35 + ICH9, 2009)", "QEMU/KVM", "", true},
		{"explicit proxmox metadata", "Proxmox", "VM", "QEMU/KVM", "Proxmox (reported by SMBIOS)", true},
		{"hyper-v guest", "Microsoft Corporation", "Virtual Machine", "Hyper-V", "", true},
		{"hyper-v capable physical system", "Microsoft Corporation", "Surface Pro", "", "", false},
		{"vmware", "VMware, Inc.", "VMware Virtual Platform", "VMware", "", true},
		{"virtualbox", "innotek GmbH", "VirtualBox", "VirtualBox", "", true},
		{"physical", "ASUSTeK COMPUTER INC.", "Desktop", "", "", false},
		{"missing identifiers", "", "", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := hardwareEnvironment(tc.manufacturer, tc.model)
			if (got != nil) != tc.vm {
				t.Fatalf("environment = %+v; want VM %v", got, tc.vm)
			}
			if got != nil && (got.Kind != "virtual-machine" || got.Hypervisor != tc.hypervisor || got.Platform != tc.platform) {
				t.Fatalf("unexpected environment: %+v", got)
			}
		})
	}
}
