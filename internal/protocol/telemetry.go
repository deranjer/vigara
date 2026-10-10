package protocol

import "time"

// TelemetrySnapshot is a point-in-time host report. Its ID is generated once
// and retained in the agent's bounded offline queue until the server accepts it.
type TelemetrySnapshot struct {
	ProtocolVersion string            `json:"protocolVersion"`
	ID              string            `json:"id"`
	AgentID         string            `json:"agentId"`
	ObservedAt      time.Time         `json:"observedAt"`
	Host            HostSummary       `json:"host"`
	Metrics         []TelemetryMetric `json:"metrics"`
	Capabilities    []string          `json:"capabilities"`
	Errors          []CollectionError `json:"errors,omitempty"`
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
