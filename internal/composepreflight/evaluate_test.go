package composepreflight

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func verifiedHost(now time.Time) HostSnapshot {
	return HostSnapshot{Source: "observed-agent", CapturedAt: now, Complete: true, Networks: []ObservedNetwork{}, Volumes: []ObservedVolume{}, Ports: []ObservedPort{}, Devices: map[string]EvidenceState{}, Environment: map[string]EvidenceState{}, RegistryAccess: map[string]EvidenceState{}, HostNetworking: EvidenceAvailable, BackupPolicies: map[string]EvidenceState{}}
}
func has(r Report, code string, status FindingStatus) bool {
	for _, f := range r.Findings {
		if f.Code == code && f.Status == status {
			return true
		}
	}
	return false
}
func TestMissingNASMountBlocksEvenWhenDirectoryExists(t *testing.T) {
	now := time.Now().UTC()
	dir := t.TempDir()
	nas := filepath.Join(dir, "mnt", "RemoteStorage")
	if err := os.MkdirAll(nas, 0755); err != nil {
		t.Fatal(err)
	}
	h := verifiedHost(now)
	h.Paths = []ObservedPath{{Path: nas, ResolvedPath: nas, Exists: true, SymlinkSafe: true, ExpectedMountPath: nas, ExpectedMountResolved: nas, MountPoint: "/"}}
	m := RequirementModel{ProjectName: "media", ProjectDirectory: dir, Mounts: []BindMount{{Source: nas, Required: true, RequireMountedFilesystem: true, ExpectedMountPath: nas}}}
	r := EvaluateObserved(m, h, now)
	if r.Ready || !has(r, "MOUNT_NOT_MOUNTED", StatusBlocked) {
		t.Fatalf("empty NAS directory passed: %#v", r)
	}
}
func TestRelativeBindPathUsesComposeProjectDirectory(t *testing.T) {
	now := time.Now().UTC()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "data"), 0755); err != nil {
		t.Fatal(err)
	}
	h := verifiedHost(now)
	resolved := filepath.Join(dir, "data")
	h.Paths = []ObservedPath{{Path: resolved, ResolvedPath: resolved, Exists: true, SymlinkSafe: true}}
	m := RequirementModel{ProjectName: "photos", ProjectDirectory: dir, Mounts: []BindMount{{Source: "./data", Required: true}}}
	r := EvaluateObserved(m, h, now)
	if !has(r, "BIND_PATH_AVAILABLE", StatusSatisfied) {
		t.Fatalf("relative source did not resolve from project directory: %#v", r)
	}
	m.Mounts[0].Source = "../../outside"
	r = EvaluateObserved(m, h, now)
	if !has(r, "BIND_PATH_INVALID", StatusBlocked) {
		t.Fatalf("escaping relative source passed: %#v", r)
	}
}
func TestExistingExternalNetworkIsPreserved(t *testing.T) {
	now := time.Now().UTC()
	h := verifiedHost(now)
	h.Networks = []ObservedNetwork{{Name: "media-shared", Driver: "bridge"}}
	m := RequirementModel{ProjectName: "media", ProjectDirectory: "/srv/stacks/media", Networks: []NetworkRequirement{{Name: "media-shared", External: true, Managed: false, Driver: "bridge"}}}
	r := EvaluateObserved(m, h, now)
	if !has(r, "NETWORK_PRESENT", StatusSatisfied) {
		t.Fatalf("existing external network not accepted: %#v", r)
	}
	if strings.Contains(strings.ToLower(strings.Join(findingMessages(r), " ")), "create") {
		t.Fatalf("report proposes mutation for existing network: %#v", r.Findings)
	}
}
func TestUnavailableDeviceBlocks(t *testing.T) {
	now := time.Now().UTC()
	h := verifiedHost(now)
	h.Devices["/dev/dri/renderD128"] = EvidenceUnavailable
	m := RequirementModel{ProjectName: "media", ProjectDirectory: "/srv/media", Devices: []DeviceRequirement{{HostPath: "/dev/dri/renderD128", Required: true}}}
	r := EvaluateObserved(m, h, now)
	if r.Ready || !has(r, "DEVICE_UNAVAILABLE", StatusBlocked) {
		t.Fatalf("unavailable device passed: %#v", r)
	}
}
func TestPortConflictAndSameProjectOwnership(t *testing.T) {
	now := time.Now().UTC()
	h := verifiedHost(now)
	h.Ports = []ObservedPort{{HostIP: "0.0.0.0", HostPort: 8096, Protocol: "tcp", ProjectName: "other", ServiceName: "server", Target: "8096", OwnerKnown: true}}
	m := RequirementModel{ProjectName: "media", ProjectDirectory: "/srv/media", ConfigurationValid: EvidenceAvailable, Ports: []PortRequirement{{HostPort: 8096, Protocol: "tcp", ServiceName: "server", Target: "8096"}}}
	r := EvaluateObserved(m, h, now)
	if r.Ready || !has(r, "PORT_CONFLICT", StatusBlocked) {
		t.Fatalf("occupied host port passed: %#v", r)
	}
	h.Ports[0].ProjectName = "media"
	h.Ports[0].ServiceName = "server"
	r = EvaluateObserved(m, h, now)
	if !has(r, "PORT_AVAILABLE", StatusSatisfied) {
		t.Fatalf("existing same-project port was flagged: %#v", r)
	}
}
func TestUnknownOrSuppliedEvidenceCannotPass(t *testing.T) {
	now := time.Now().UTC()
	h := verifiedHost(now)
	h.Source = "supplied"
	m := RequirementModel{ProjectName: "media", ProjectDirectory: "/srv/media", Networks: []NetworkRequirement{{Name: "media-net", External: true}}}
	r := Evaluate(m, h, now)
	if r.Ready || r.EvidenceSource != "supplied" || !has(r, "HOST_EVIDENCE_UNKNOWN", StatusUnknown) {
		t.Fatalf("supplied evidence was treated as observation: %#v", r)
	}
}
func TestDiagnosticsDoNotIncludeSecretValuesOrRawError(t *testing.T) {
	now := time.Now().UTC()
	h := verifiedHost(now)
	h.CollectionError = true
	m := RequirementModel{ProjectName: "media", ProjectDirectory: "/srv/media", RequiredEnvironment: []EnvironmentRequirement{{Name: "DB_PASSWORD", State: EvidenceUnavailable}}}
	r := EvaluateObserved(m, h, now)
	b := strings.ToLower(strings.Join(findingMessages(r), " "))
	if strings.Contains(b, "hunter2") || strings.Contains(b, "password=") {
		t.Fatalf("secret-like content leaked: %s", b)
	}
	if strings.Contains(strings.ToLower(strings.Join(findingMessages(r), " ")), "error:") {
		t.Fatal("raw error detail appeared")
	}
}
func findingMessages(r Report) []string {
	var out []string
	for _, f := range r.Findings {
		out = append(out, f.Message)
	}
	return out
}

func TestUnrelatedParentMountDoesNotSatisfyNASRequirement(t *testing.T) {
	now := time.Now().UTC()
	dir := t.TempDir()
	source := filepath.Join(dir, "mnt", "data", "app")
	h := verifiedHost(now)
	h.Paths = []ObservedPath{{Path: source, ResolvedPath: source, Exists: true, SymlinkSafe: true, ExpectedMountPath: filepath.Join(dir, "mnt", "data"), ExpectedMountResolved: filepath.Join(dir, "mnt", "data"), MountPoint: filepath.Join(dir, "home"), Filesystem: "ext4"}}
	m := RequirementModel{ProjectName: "media", ProjectDirectory: dir, Mounts: []BindMount{{Source: source, Required: true, RequireMountedFilesystem: true, ExpectedMountPath: filepath.Join(dir, "mnt", "data")}}}
	r := EvaluateObserved(m, h, now)
	if r.Ready || !has(r, "MOUNT_NOT_MOUNTED", StatusBlocked) {
		t.Fatalf("unrelated parent filesystem passed as NAS: %#v", r)
	}
}
func TestUnknownHostNetworkingAndMissingDriverRemainUnknown(t *testing.T) {
	now := time.Now().UTC()
	h := verifiedHost(now)
	h.HostNetworking = EvidenceState("maybe")
	h.Networks = []ObservedNetwork{{Name: "shared"}}
	m := RequirementModel{ProjectName: "media", ProjectDirectory: "/srv/media", HostNetwork: true, Networks: []NetworkRequirement{{Name: "shared", External: true, Driver: "bridge"}}}
	r := EvaluateObserved(m, h, now)
	if r.Ready || !has(r, "HOST_NETWORK_UNKNOWN", StatusUnknown) || !has(r, "NETWORK_DRIVER_UNKNOWN", StatusUnknown) {
		t.Fatalf("invalid capability evidence passed: %#v", r)
	}
}

func TestDuplicateModelPortsAndEquivalentIPv6Conflict(t *testing.T) {
	now := time.Now().UTC()
	h := verifiedHost(now)
	h.Ports = []ObservedPort{}
	m := RequirementModel{ProjectName: "media", ProjectDirectory: "/srv/media", Ports: []PortRequirement{{HostIP: "::ffff:192.0.2.10", HostPort: 8080, Protocol: "tcp", ServiceName: "one", Target: "80"}, {HostIP: "192.0.2.10", HostPort: 8080, Protocol: "tcp", ServiceName: "two", Target: "80"}}}
	r := EvaluateObserved(m, h, now)
	if !has(r, "PORT_CONFIGURATION_CONFLICT", StatusBlocked) {
		t.Fatalf("same-model duplicate binds passed: %#v", r)
	}
}
func TestExpectedMountpointSymlinkIsRejected(t *testing.T) {
	now := time.Now().UTC()
	dir := t.TempDir()
	source := filepath.Join(dir, "data")
	expected := filepath.Join(dir, "nas")
	h := verifiedHost(now)
	h.Paths = []ObservedPath{{Path: source, ResolvedPath: source, Exists: true, SymlinkSafe: true, ExpectedMountPath: expected, ExpectedMountResolved: filepath.Join(dir, "home"), MountPoint: filepath.Join(dir, "home")}}
	m := RequirementModel{ProjectName: "media", ProjectDirectory: dir, Mounts: []BindMount{{Source: source, Required: true, RequireMountedFilesystem: true, ExpectedMountPath: expected}}}
	r := EvaluateObserved(m, h, now)
	if r.Ready || !has(r, "MOUNT_NOT_MOUNTED", StatusBlocked) {
		t.Fatalf("symlinked expected NAS mount passed: %#v", r)
	}
}
func TestInvalidHostIPDoesNotPass(t *testing.T) {
	now := time.Now().UTC()
	h := verifiedHost(now)
	m := RequirementModel{ProjectName: "media", ProjectDirectory: "/srv/media", Ports: []PortRequirement{{HostIP: "not-an-ip", HostPort: 8080, Protocol: "tcp", ServiceName: "web", Target: "80"}}}
	r := EvaluateObserved(m, h, now)
	if r.Ready || !has(r, "PORT_REQUIREMENT_INVALID", StatusBlocked) {
		t.Fatalf("invalid host IP passed: %#v", r)
	}
}

func TestRegistryIdentityRejectsCredentialsAndPaths(t *testing.T) {
	now := time.Now().UTC()
	h := verifiedHost(now)
	h.RegistryAccess["registry.example"] = EvidenceAvailable
	m := RequirementModel{ProjectName: "media", ProjectDirectory: "/srv/media", RequiredRegistries: []string{"user:secret@registry.example/repo"}}
	r := EvaluateObserved(m, h, now)
	if r.Ready || !has(r, "REGISTRY_REQUIREMENT_INVALID", StatusBlocked) {
		t.Fatalf("credential-bearing registry input passed: %#v", r)
	}
	for _, f := range r.Findings {
		if strings.Contains(f.Resource, "secret") || strings.Contains(strings.ToLower(f.Message), "user:") {
			t.Fatalf("registry credential leaked: %#v", f)
		}
	}
}
func TestProjectNameMustMatchComposeIdentityContract(t *testing.T) {
	now := time.Now().UTC()
	h := verifiedHost(now)
	m := RequirementModel{ProjectName: "bad name", ProjectDirectory: "/srv/media"}
	r := EvaluateObserved(m, h, now)
	if r.Ready || !has(r, "COMPOSE_PROJECT_IDENTITY_INVALID", StatusBlocked) {
		t.Fatalf("invalid project name passed: %#v", r)
	}
}

func TestConfigurationAssetsStaySeparateAndPersistentDataNeedsBackup(t *testing.T) {
	now := time.Now().UTC()
	h := verifiedHost(now)
	m := RequirementModel{ProjectName: "media", ProjectDirectory: "/srv/media", ConfigurationAssets: []AssetRequirement{{Path: "config/nginx.conf", Class: AssetPortableConfig, Required: true, State: EvidenceAvailable}, {Path: "/mnt/data", Class: AssetPersistentData, Required: true}}}
	r := EvaluateObserved(m, h, now)
	if !has(r, "PORTABLE_CONFIG_AVAILABLE", StatusSatisfied) || !has(r, "BACKUP_POLICY_UNASSIGNED", StatusBlocked) {
		t.Fatalf("asset classes or backup policy were not assessed: %#v", r)
	}
}

func TestLinuxHostFilePathIsComparedUsingTargetPathRules(t *testing.T) {
	now := time.Now().UTC()
	h := verifiedHost(now)
	const hostPath = "/srv/media/config/nginx.conf"
	h.Paths = []ObservedPath{{Path: hostPath, ResolvedPath: hostPath, Exists: true, SymlinkSafe: true}}
	m := RequirementModel{ProjectName: "media", ProjectDirectory: "/srv/media", ConfigurationAssets: []AssetRequirement{{Path: hostPath, Class: AssetHostFile, Required: true}}}
	r := EvaluateObserved(m, h, now)
	if !has(r, "HOST_FILE_AVAILABLE", StatusSatisfied) {
		t.Fatalf("Linux target path was not recognized: %#v", r)
	}
}

func TestPersistentVolumeWithoutPolicyBlocks(t *testing.T) {
	now := time.Now().UTC()
	h := verifiedHost(now)
	h.Volumes = []ObservedVolume{{Name: "media-data", Driver: "local"}}
	m := RequirementModel{ProjectName: "media", ProjectDirectory: "/srv/media", Volumes: []VolumeRequirement{{Name: "media-data", External: true, Persistent: true, Driver: "local"}}}
	r := EvaluateObserved(m, h, now)
	if r.Ready || !has(r, "BACKUP_POLICY_UNASSIGNED", StatusBlocked) {
		t.Fatalf("persistent volume without policy passed: %#v", r)
	}
}
