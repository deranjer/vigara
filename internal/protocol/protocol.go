// Package protocol contains the versioned JSON wire contract shared by server
// and agent. Changes to these types are compatibility changes; see docs/protocol.md.
package protocol

import "time"

// CurrentVersion identifies the major wire contract. It is exchanged on every
// agent request; incompatible changes require a new major version.
const CurrentVersion = "v1"

// AgentRequest is sent by an enrolled agent when polling for current desired
// state and work. AgentID is the stable server-issued device identity.
type AgentRequest struct {
	ProtocolVersion        string   `json:"protocolVersion"`
	AgentID                string   `json:"agentId"`
	AgentVersion           string   `json:"agentVersion"`
	Platform               string   `json:"platform"`
	Architecture           string   `json:"architecture"`
	Capabilities           []string `json:"capabilities"`
	KnownConfigVersion     string   `json:"knownConfigVersion,omitempty"`
	LastSeenJobID          string   `json:"lastSeenJobId,omitempty"`
	SupportsBackupProgress bool     `json:"supportsBackupProgress,omitempty"`
}

// AgentResponse returns the latest configuration and at most one authorized
// job. Empty Job means no work is currently available.
type AgentResponse struct {
	ProtocolVersion       string              `json:"protocolVersion"`
	ServerTime            time.Time           `json:"serverTime"`
	PollAfterSeconds      int                 `json:"pollAfterSeconds"`
	Configuration         *AgentConfiguration `json:"configuration,omitempty"`
	Job                   *Job                `json:"job,omitempty"`
	BackupProgressEnabled bool                `json:"backupProgressEnabled,omitempty"`
}

// AgentConfiguration is a complete desired-state snapshot, not a patch.
type AgentConfiguration struct {
	Version     string                `json:"version"`
	GeneratedAt time.Time             `json:"generatedAt"`
	Modules     []ModuleConfiguration `json:"modules"`
}

type ModuleConfiguration struct {
	ModuleID      string         `json:"moduleId"`
	ModuleVersion string         `json:"moduleVersion"`
	Enabled       bool           `json:"enabled"`
	Settings      map[string]any `json:"settings,omitempty"`
}

// Job has a stable server-generated ID and immutable module identity/version.
// TimeoutSeconds is a hard execution budget; agents must report timeout as a
// failed result and continue polling rather than stopping the agent process.
type Job struct {
	ID             string         `json:"id"`
	AttemptToken   string         `json:"attemptToken,omitempty"`
	ModuleID       string         `json:"moduleId"`
	ModuleVersion  string         `json:"moduleVersion"`
	Kind           string         `json:"kind"`
	CreatedAt      time.Time      `json:"createdAt"`
	Deadline       time.Time      `json:"deadline"`
	TimeoutSeconds int            `json:"timeoutSeconds"`
	Settings       map[string]any `json:"settings,omitempty"`
	Permissions    []Permission   `json:"permissions,omitempty"`
}

// Permission declares a capability explicitly granted for this job. Modules
// must not infer permissions from configuration or request broader access.
type Permission struct {
	Name  string `json:"name"`
	Scope string `json:"scope,omitempty"`
}

// CapabilityReport describes what this particular agent build can execute.
type CapabilityReport struct {
	ProtocolVersion string   `json:"protocolVersion"`
	Platform        string   `json:"platform"`
	Architecture    string   `json:"architecture"`
	AgentVersion    string   `json:"agentVersion"`
	Capabilities    []string `json:"capabilities"`
}

// TelemetryBatch is an idempotently ingestible batch. Event IDs remain stable
// across retries; timestamps are UTC instants and values use declared units.
type TelemetryBatch struct {
	ProtocolVersion string           `json:"protocolVersion"`
	AgentID         string           `json:"agentId"`
	Events          []TelemetryEvent `json:"events"`
}

type TelemetryEvent struct {
	ID         string            `json:"id"`
	Metric     string            `json:"metric"`
	Value      float64           `json:"value"`
	Unit       string            `json:"unit"`
	ObservedAt time.Time         `json:"observedAt"`
	Labels     map[string]string `json:"labels,omitempty"`
}

// JobResult is submitted once work has completed. Result IDs and JobID are
// stable across retries so the server can deduplicate delivery.
type JobResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	ID              string         `json:"id"`
	JobID           string         `json:"jobId"`
	AttemptToken    string         `json:"attemptToken,omitempty"`
	AgentID         string         `json:"agentId"`
	ModuleID        string         `json:"moduleId"`
	ModuleVersion   string         `json:"moduleVersion"`
	Status          JobStatus      `json:"status"`
	StartedAt       time.Time      `json:"startedAt"`
	FinishedAt      time.Time      `json:"finishedAt"`
	Output          map[string]any `json:"output,omitempty"`
	Error           *Failure       `json:"error,omitempty"`
}

// BackupProgress is a bounded, aggregate-only heartbeat for one leased backup
// attempt. It must never contain paths, file names, command output, or secrets.
type BackupProgress struct {
	ProtocolVersion string    `json:"protocolVersion"`
	ID              string    `json:"id"`
	JobID           string    `json:"jobId"`
	AttemptToken    string    `json:"attemptToken"`
	AgentID         string    `json:"agentId"`
	Sequence        uint64    `json:"sequence"`
	ObservedAt      time.Time `json:"observedAt"`
	Phase           string    `json:"phase"`
	FilesDone       uint64    `json:"filesDone,omitempty"`
	FilesTotal      uint64    `json:"filesTotal,omitempty"`
	BytesDone       uint64    `json:"bytesDone,omitempty"`
	BytesTotal      uint64    `json:"bytesTotal,omitempty"`
	PercentDone     float64   `json:"percentDone,omitempty"`
}

type JobStatus string

const (
	JobSucceeded JobStatus = "succeeded"
	JobFailed    JobStatus = "failed"
	JobTimedOut  JobStatus = "timed_out"
	JobCancelled JobStatus = "cancelled"
)

type Failure struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}
