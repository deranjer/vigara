package protocol

// AgentUpdatePlan names an immutable package served by the enrolled server.
// Paths and service names are local constants, never supplied by the server.
type AgentUpdatePlan struct {
	Version    string `json:"version"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	Generation int64  `json:"generation"`
}

type AgentUpdateStatus struct {
	State      string `json:"state"`
	Version    string `json:"version,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	Generation int64  `json:"generation,omitempty"`
	Message    string `json:"message,omitempty"`
}
