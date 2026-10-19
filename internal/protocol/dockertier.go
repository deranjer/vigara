package protocol

import (
	"fmt"
	"strings"
)

// Docker access tiers. An agent offers a tier only when its local operator sets
// VIGIRA_DOCKER_MODE; the server honours it only when the agent's enrollment
// scope also includes the matching capability. Higher tiers include lower ones.
const (
	DockerModeRead   = "read"
	DockerModeManage = "manage"
	DockerModeExec   = "exec"
	DockerModeDeploy = "deploy"

	CapabilityDockerManage = "docker.manage"
	CapabilityDockerExec   = "docker.exec"
	CapabilityDockerDeploy = "docker.deploy"
)

var dockerTierCapabilities = map[string][]string{
	DockerModeRead:   {},
	DockerModeManage: {CapabilityDockerManage},
	DockerModeExec:   {CapabilityDockerManage, CapabilityDockerExec},
	DockerModeDeploy: {CapabilityDockerManage, CapabilityDockerExec, CapabilityDockerDeploy},
}

// DockerTierCapabilities returns the capabilities a local VIGIRA_DOCKER_MODE
// value offers. An empty mode offers none; read is the collector's existing
// behaviour and needs no capability.
func DockerTierCapabilities(mode string) ([]string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return nil, nil
	}
	capabilities, ok := dockerTierCapabilities[mode]
	if !ok {
		return nil, fmt.Errorf("VIGIRA_DOCKER_MODE must be read, manage, exec, or deploy")
	}
	return append([]string(nil), capabilities...), nil
}

// DockerTier returns the highest Docker access tier implied by a capability list.
func DockerTier(capabilities []string) string {
	has := map[string]bool{}
	for _, capability := range capabilities {
		has[capability] = true
	}
	switch {
	case has[CapabilityDockerDeploy]:
		return DockerModeDeploy
	case has[CapabilityDockerExec]:
		return DockerModeExec
	case has[CapabilityDockerManage]:
		return DockerModeManage
	}
	return DockerModeRead
}

// DockerTierRank orders tiers so that callers can compare required and granted access.
func DockerTierRank(tier string) int {
	switch tier {
	case DockerModeDeploy:
		return 3
	case DockerModeExec:
		return 2
	case DockerModeManage:
		return 1
	}
	return 0
}
