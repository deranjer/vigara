// Package composepreflight builds a non-mutating readiness report from a reduced,
// secret-free Compose model and explicit host evidence. It never applies changes.
package composepreflight

import "time"

type EvidenceState string

const (
	EvidenceAvailable   EvidenceState = "available"
	EvidenceUnavailable EvidenceState = "unavailable"
	EvidenceUnknown     EvidenceState = "unknown"
)

type Severity string

const (
	SeverityBlocker Severity = "blocker"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

type FindingStatus string

const (
	StatusSatisfied FindingStatus = "satisfied"
	StatusPlanned   FindingStatus = "planned"
	StatusBlocked   FindingStatus = "blocked"
	StatusUnknown   FindingStatus = "unknown"
)

type RequirementModel struct {
	ProjectName         string                   `json:"projectName"`
	ProjectDirectory    string                   `json:"projectDirectory"`
	ComposeVersion      string                   `json:"composeVersion,omitempty"`
	DockerVersion       string                   `json:"dockerVersion,omitempty"`
	ConfigurationValid  EvidenceState            `json:"configurationValid"`
	RequiredEnvironment []EnvironmentRequirement `json:"requiredEnvironment,omitempty"`
	RequiredRegistries  []string                 `json:"requiredRegistries,omitempty"`
	Networks            []NetworkRequirement     `json:"networks,omitempty"`
	Volumes             []VolumeRequirement      `json:"volumes,omitempty"`
	Mounts              []BindMount              `json:"mounts,omitempty"`
	Ports               []PortRequirement        `json:"ports,omitempty"`
	HostNetwork         bool                     `json:"hostNetwork,omitempty"`
	Devices             []DeviceRequirement      `json:"devices,omitempty"`
	BackupPolicies      []BackupRequirement      `json:"backupPolicies,omitempty"`
	ConfigurationAssets []AssetRequirement       `json:"configurationAssets,omitempty"`
}

// EnvironmentRequirement records names and availability only. Values are never accepted.
type EnvironmentRequirement struct {
	Name  string        `json:"name"`
	State EvidenceState `json:"state"`
}
type NetworkRequirement struct {
	Name       string `json:"name"`
	External   bool   `json:"external"`
	Managed    bool   `json:"managed"`
	Driver     string `json:"driver,omitempty"`
	Scope      string `json:"scope,omitempty"`
	Internal   *bool  `json:"internal,omitempty"`
	Attachable *bool  `json:"attachable,omitempty"`
	Purpose    string `json:"purpose,omitempty"`
}
type VolumeRequirement struct {
	Name           string `json:"name"`
	External       bool   `json:"external"`
	Managed        bool   `json:"managed"`
	Driver         string `json:"driver,omitempty"`
	BackupPolicyID string `json:"backupPolicyId,omitempty"`
	Persistent     bool   `json:"persistent,omitempty"`
}
type BindMount struct {
	Source                   string `json:"source"`
	Target                   string `json:"target"`
	Required                 bool   `json:"required"`
	RequireMountedFilesystem bool   `json:"requireMountedFilesystem,omitempty"`
	ExpectedMountPath        string `json:"expectedMountPath,omitempty"`
	Purpose                  string `json:"purpose,omitempty"`
	BackupPolicyID           string `json:"backupPolicyId,omitempty"`
	Persistent               bool   `json:"persistent,omitempty"`
	Device                   bool   `json:"device,omitempty"`
}
type PortRequirement struct {
	HostIP      string `json:"hostIp,omitempty"`
	HostPort    uint16 `json:"hostPort"`
	Protocol    string `json:"protocol,omitempty"`
	ServiceName string `json:"serviceName,omitempty"`
	Target      string `json:"target,omitempty"`
}
type DeviceRequirement struct {
	HostPath      string `json:"hostPath"`
	ContainerPath string `json:"containerPath,omitempty"`
	Required      bool   `json:"required"`
}
type AssetClass string

const (
	AssetPortableConfig AssetClass = "portable-config"
	AssetPersistentData AssetClass = "persistent-data"
	AssetHostFile       AssetClass = "host-file"
	AssetHostDevice     AssetClass = "host-device"
)

type AssetRequirement struct {
	Path           string        `json:"path"`
	Class          AssetClass    `json:"class"`
	Required       bool          `json:"required"`
	State          EvidenceState `json:"state,omitempty"` // availability only; no content
	BackupPolicyID string        `json:"backupPolicyId,omitempty"`
	Purpose        string        `json:"purpose,omitempty"`
}
type BackupRequirement struct {
	Resource string `json:"resource"`
	PolicyID string `json:"policyId"`
}

type HostSnapshot struct {
	Source          string                   `json:"source"` // observed-agent, or supplied
	CapturedAt      time.Time                `json:"capturedAt"`
	Complete        bool                     `json:"complete"`
	CollectionError bool                     `json:"collectionError,omitempty"` // deliberately has no raw error text
	DockerVersion   string                   `json:"dockerVersion,omitempty"`
	ComposeVersion  string                   `json:"composeVersion,omitempty"`
	Networks        []ObservedNetwork        `json:"networks,omitempty"`
	Volumes         []ObservedVolume         `json:"volumes,omitempty"`
	Paths           []ObservedPath           `json:"paths,omitempty"`
	Ports           []ObservedPort           `json:"ports,omitempty"`
	Devices         map[string]EvidenceState `json:"devices,omitempty"`
	Environment     map[string]EvidenceState `json:"environment,omitempty"`
	RegistryAccess  map[string]EvidenceState `json:"registryAccess,omitempty"`
	HostNetworking  EvidenceState            `json:"hostNetworking"`
	BackupPolicies  map[string]EvidenceState `json:"backupPolicies,omitempty"`
}
type ObservedPath struct {
	Path                  string `json:"path"`
	ResolvedPath          string `json:"resolvedPath,omitempty"`
	Exists                bool   `json:"exists"`
	SymlinkSafe           bool   `json:"symlinkSafe"`
	MountPoint            string `json:"mountPoint,omitempty"`
	ExpectedMountPath     string `json:"expectedMountPath,omitempty"`
	ExpectedMountResolved string `json:"expectedMountResolved,omitempty"`
	Filesystem            string `json:"filesystem,omitempty"`
}
type ObservedNetwork struct {
	Name       string            `json:"name"`
	Driver     string            `json:"driver,omitempty"`
	Scope      string            `json:"scope,omitempty"`
	Internal   *bool             `json:"internal,omitempty"`
	Attachable *bool             `json:"attachable,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
}
type ObservedVolume struct {
	Name   string            `json:"name"`
	Driver string            `json:"driver,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
}
type ObservedPort struct {
	HostIP      string `json:"hostIp,omitempty"`
	HostPort    uint16 `json:"hostPort"`
	Protocol    string `json:"protocol,omitempty"`
	ProjectName string `json:"projectName,omitempty"`
	ServiceName string `json:"serviceName,omitempty"`
	Target      string `json:"target,omitempty"`
	OwnerKnown  bool   `json:"ownerKnown"`
}

type Finding struct {
	Code     string        `json:"code"`
	Status   FindingStatus `json:"status"`
	Severity Severity      `json:"severity"`
	Resource string        `json:"resource,omitempty"`
	Message  string        `json:"message"`
}
type Report struct {
	ProjectName        string     `json:"projectName"`
	GeneratedAt        time.Time  `json:"generatedAt"`
	EvidenceSource     string     `json:"evidenceSource"`
	EvidenceCapturedAt *time.Time `json:"evidenceCapturedAt,omitempty"`
	Ready              bool       `json:"ready"`
	Findings           []Finding  `json:"findings"`
}
