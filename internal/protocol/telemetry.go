package protocol

import "time"

// TelemetrySnapshot is a point-in-time host report. Its ID is generated once
// and retained in the agent's bounded offline queue until the server accepts it.
type TelemetrySnapshot struct {
	ProtocolVersion   string             `json:"protocolVersion"`
	ID                string             `json:"id"`
	AgentID           string             `json:"agentId"`
	ObservedAt        time.Time          `json:"observedAt"`
	Host              HostSummary        `json:"host"`
	HardwareInventory *HardwareInventory `json:"hardwareInventory,omitempty"`
	Metrics           []TelemetryMetric  `json:"metrics"`
	DockerContainers  []DockerContainer  `json:"dockerContainers,omitempty"`
	Capabilities      []string           `json:"capabilities"`
	Errors            []CollectionError  `json:"errors,omitempty"`
}

// HardwareInventory is a bounded, point-in-time inventory separate from
// time-series metrics. Status values are available, partial, unsupported, or
// unavailable; unavailable fields are intentionally left empty.
type HardwareInventory struct {
	ObservedAt    time.Time      `json:"observedAt"`
	CPUStatus     string         `json:"cpuStatus"`
	CPU           HardwareCPU    `json:"cpu"`
	GPUStatus     string         `json:"gpuStatus"`
	GPUs          []HardwareGPU  `json:"gpus"`
	MemoryStatus  string         `json:"memoryStatus"`
	MemoryModules []MemoryModule `json:"memoryModules"`
	BoardStatus   string         `json:"boardStatus"`
	Motherboard   Motherboard    `json:"motherboard"`
	Firmware      Firmware       `json:"firmware"`
	DiskStatus    string         `json:"diskStatus"`
	Disks         []HardwareDisk `json:"disks"`
}

type HardwareCPU struct {
	Vendor           string `json:"vendor,omitempty"`
	Model            string `json:"model,omitempty"`
	PhysicalPackages int    `json:"physicalPackages,omitempty"`
	PhysicalCores    int    `json:"physicalCores,omitempty"`
	LogicalThreads   int    `json:"logicalThreads,omitempty"`
}

type HardwareGPU struct {
	Vendor    string `json:"vendor,omitempty"`
	Model     string `json:"model,omitempty"`
	VRAMBytes uint64 `json:"vramBytes,omitempty"`
}

type MemoryModule struct {
	CapacityBytes uint64 `json:"capacityBytes,omitempty"`
	Type          string `json:"type,omitempty"`
	SpeedMHz      uint64 `json:"speedMHz,omitempty"`
}

type Motherboard struct {
	Manufacturer string `json:"manufacturer,omitempty"`
	Model        string `json:"model,omitempty"`
}
type Firmware struct {
	Manufacturer string `json:"manufacturer,omitempty"`
	Version      string `json:"version,omitempty"`
	ReleaseDate  string `json:"releaseDate,omitempty"`
}
type HardwareDisk struct {
	Model         string `json:"model,omitempty"`
	CapacityBytes uint64 `json:"capacityBytes,omitempty"`
	Type          string `json:"type,omitempty"`
}

// DockerContainer is bounded inventory metadata from an explicitly enabled
// read-only Docker collector. Environment variables and container config are
// deliberately excluded.
type DockerContainer struct {
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	Image string `json:"image,omitempty"`
	State string `json:"state"`
}

type TelemetrySnapshotBatch struct {
	ProtocolVersion string              `json:"protocolVersion"`
	AgentID         string              `json:"agentId"`
	Snapshots       []TelemetrySnapshot `json:"snapshots"`
}

type HostSummary struct {
	OS              string `json:"os"`
	OSVersion       string `json:"osVersion,omitempty"`
	Architecture    string `json:"architecture"`
	AgentVersion    string `json:"agentVersion"`
	CPUModel        string `json:"cpuModel,omitempty"`
	LogicalCPUs     int    `json:"logicalCpus,omitempty"`
	TotalMemoryByte uint64 `json:"totalMemoryBytes,omitempty"`
	UptimeSeconds   uint64 `json:"uptimeSeconds,omitempty"`
}

type TelemetryMetric struct {
	Name   string            `json:"name"`
	Value  float64           `json:"value"`
	Unit   string            `json:"unit"`
	Labels map[string]string `json:"labels,omitempty"`
}

type CollectionError struct {
	Capability string `json:"capability"`
	Message    string `json:"message"`
}
