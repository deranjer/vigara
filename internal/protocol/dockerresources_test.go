package protocol

import (
	"strings"
	"testing"
)

func TestResourceRefValidation(t *testing.T) {
	for _, tc := range []struct {
		kind, ref string
		ok        bool
	}{
		{KindImage, "nginx", true}, {KindImage, "nginx:1.27-alpine", true}, {KindImage, "ghcr.io/deranjer/vigara:staging", true},
		{KindImage, "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", true},
		{KindImage, "repo/app@sha256:abc123", true}, {KindImage, "localhost:5000/team/app:v1", true},
		{KindImage, "", false}, {KindImage, "-rf", false}, {KindImage, "a b", false}, {KindImage, "a;b", false}, {KindImage, "../etc", false},
		{KindImage, "repo//app", false}, {KindImage, "repo/./app", false}, {KindImage, "repo/../app", false}, {KindImage, "repo/", false},
		{KindImage, strings.Repeat("a", 256), false}, {KindImage, "app\nname", false}, {KindImage, "app?x=1", false}, {KindImage, "app%2Fx", false},
		{KindNetwork, "bridge", true}, {KindNetwork, "my-net_1.x", true}, {KindNetwork, "0123456789ab", true},
		{KindNetwork, "a/b", false}, {KindNetwork, "-x", false}, {KindNetwork, "a b", false}, {KindNetwork, "", false},
		{KindVolume, "pgdata", true}, {KindVolume, "site_db-data.1", true}, {KindVolume, strings.Repeat("a", 64), true},
		{KindVolume, "/var/lib", false}, {KindVolume, "a/b", false}, {KindVolume, "..", false}, {KindVolume, "", false},
		{"container", "web", false}, {"", "web", false},
	} {
		if got := ValidResourceRef(tc.kind, tc.ref); got != tc.ok {
			t.Errorf("ValidResourceRef(%q, %q) = %v, want %v", tc.kind, tc.ref, got, tc.ok)
		}
	}
	if !ValidResourceKind(KindImage) || !ValidResourceKind(KindNetwork) || !ValidResourceKind(KindVolume) || ValidResourceKind("secret") {
		t.Fatal("ValidResourceKind")
	}
}

func TestResourceMethodTiers(t *testing.T) {
	for method, want := range map[string]string{
		MethodDockerImages: "", MethodDockerNetworks: "", MethodDockerVolumes: "", MethodDockerResourceInspect: "",
		MethodDockerPull: DockerModeManage, MethodDockerResourceRemove: DockerModeManage, MethodDockerPrune: DockerModeManage,
	} {
		if tier, known := ChannelMethodTier(method); !known || tier != want {
			t.Errorf("%s tier = %q known=%v, want %q", method, tier, known, want)
		}
	}
}
