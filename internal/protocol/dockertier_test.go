package protocol

import (
	"reflect"
	"testing"
)

func TestDockerTierCapabilitiesAndRanking(t *testing.T) {
	for mode, want := range map[string][]string{
		"":       nil,
		"read":   {},
		"Manage": {CapabilityDockerManage},
		" exec ": {CapabilityDockerManage, CapabilityDockerExec},
		"deploy": {CapabilityDockerManage, CapabilityDockerExec, CapabilityDockerDeploy},
	} {
		got, err := DockerTierCapabilities(mode)
		if err != nil || len(got) != len(want) || (len(want) > 0 && !reflect.DeepEqual(got, want)) {
			t.Errorf("DockerTierCapabilities(%q) = %v, %v; want %v", mode, got, err, want)
		}
	}
	if _, err := DockerTierCapabilities("root"); err == nil {
		t.Error("accepted unknown mode")
	}
	for capabilities, want := range map[string]string{"": DockerModeRead, "docker.manage": DockerModeManage, "docker.exec": DockerModeExec, "docker.deploy": DockerModeDeploy, "restic.backup": DockerModeRead} {
		if got := DockerTier([]string{capabilities}); got != want {
			t.Errorf("DockerTier(%q) = %q, want %q", capabilities, got, want)
		}
	}
	if DockerTierRank(DockerModeDeploy) <= DockerTierRank(DockerModeExec) || DockerTierRank(DockerModeExec) <= DockerTierRank(DockerModeManage) || DockerTierRank(DockerModeManage) <= DockerTierRank(DockerModeRead) {
		t.Error("tier ranks are not strictly increasing")
	}
}
