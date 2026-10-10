// Package modules defines contracts for built-in collectors and providers.
// Implementations are invoked independently with a bounded context; see
// docs/protocol.md for lifecycle and compatibility requirements.
package modules

import (
	"context"
	"time"
)

// Descriptor is the stable, discoverable identity and declared surface of a
// module. ID must not change between releases; Version follows SemVer.
type Descriptor struct {
	ID            string           `json:"id"`
	Version       string           `json:"version"`
	Platforms     []string         `json:"platforms"`
	Capabilities  []string         `json:"capabilities"`
	Configuration []ConfigField    `json:"configuration,omitempty"`
	Permissions   []PermissionSpec `json:"permissions,omitempty"`
	Metrics       []MetricSpec     `json:"metrics,omitempty"`
}

// ConfigField documents an accepted setting. Required settings must be
// validated before Execute is called; Secret values must not be logged.
type ConfigField struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Required    bool   `json:"required,omitempty"`
	Secret      bool   `json:"secret,omitempty"`
	Description string `json:"description,omitempty"`
}

// PermissionSpec is a capability a module may need. Runtime grants are
// separately attached to each job and must be checked before access.
type PermissionSpec struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// MetricSpec defines a stable metric key and its canonical unit. Units use
// UCUM-compatible spellings where practical (for example "Cel", "%", "By").
type MetricSpec struct {
	Name        string `json:"name"`
	Unit        string `json:"unit"`
	Description string `json:"description,omitempty"`
}

// Module is the execution boundary for one collector or provider. The runner
// supplies a context with the job deadline and enforces per-module failure
// isolation: an error, panic, or timeout is recorded for this invocation and
// must not terminate the agent or prevent other modules from running.
type Module interface {
	Descriptor() Descriptor
	Execute(context.Context, JobContext) (Result, error)
}

// JobContext contains only the job inputs and grants for this invocation.
type JobContext struct {
	JobID         string
	Configuration map[string]any
	Permissions   []PermissionGrant
	StartedAt     time.Time
}

type PermissionGrant struct {
	Name  string
	Scope string
}

type Result struct {
	Output  map[string]any
	Metrics []Metric
}

type Metric struct {
	Name       string
	Value      float64
	Unit       string
	ObservedAt time.Time
	Labels     map[string]string
}
