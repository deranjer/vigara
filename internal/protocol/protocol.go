// Package protocol contains versioned wire types shared by server and agent.
package protocol

const CurrentVersion = "v1"

type CapabilityReport struct {
	ProtocolVersion string   `json:"protocolVersion"`
	Platform        string   `json:"platform"`
	Architecture    string   `json:"architecture"`
	AgentVersion    string   `json:"agentVersion"`
	Capabilities    []string `json:"capabilities"`
}
