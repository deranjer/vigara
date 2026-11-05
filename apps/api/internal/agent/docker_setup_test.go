package agent

import (
	"github.com/deranjer/vigara/internal/protocol"
	"slices"
	"testing"
)

func TestDockerInstallerSettings(t *testing.T) {
	for _, tc := range []struct {
		name, target string
		settings     dockerSetup
		valid        bool
	}{
		{"socket viewing", "linux-amd64", dockerSetup{Mode: "read", Host: "unix:///var/run/docker.sock"}, true},
		{"loopback management", "windows-amd64", dockerSetup{Mode: "manage", Host: "http://127.0.0.1:2375"}, true},
		{"ipv6", "windows-amd64", dockerSetup{Mode: "exec", Host: "http://[::1]:2375"}, true},
		{"deploy defaults", "linux-arm64", dockerSetup{Mode: "deploy", Host: "unix:///var/run/docker.sock"}, true},
		{"custom compose root", "linux-amd64", dockerSetup{Mode: "deploy", Host: "unix:///var/run/docker.sock", ManagedRoot: "/home/operator/shared stacks"}, true},
		{"named stack owner", "linux-amd64", dockerSetup{Mode: "deploy", Host: "unix:///var/run/docker.sock", StackOwner: "deranjer:docker"}, true},
		{"numeric stack owner", "linux-amd64", dockerSetup{Mode: "deploy", Host: "unix:///var/run/docker.sock", StackOwner: "1000:1000"}, true},
		{"owner injection", "linux-amd64", dockerSetup{Mode: "deploy", Host: "unix:///var/run/docker.sock", StackOwner: "root\nUser=root"}, false},
		{"owner command", "linux-amd64", dockerSetup{Mode: "deploy", Host: "unix:///var/run/docker.sock", StackOwner: "root;id"}, false},
		{"owner without deploy", "linux-amd64", dockerSetup{Mode: "read", Host: "unix:///var/run/docker.sock", StackOwner: "deranjer"}, false},
		{"remote", "linux-amd64", dockerSetup{Mode: "read", Host: "http://192.168.1.2:2375"}, false},
		{"credential", "linux-amd64", dockerSetup{Mode: "read", Host: "http://name:secret@127.0.0.1:2375"}, false},
		{"query", "linux-amd64", dockerSetup{Mode: "read", Host: "http://127.0.0.1:2375?x=1"}, false},
		{"missing port", "linux-amd64", dockerSetup{Mode: "read", Host: "http://127.0.0.1"}, false},
		{"unsupported pipe", "windows-amd64", dockerSetup{Mode: "read", Host: "npipe:////./pipe/docker_engine"}, false},
		{"Windows unix", "windows-amd64", dockerSetup{Mode: "read", Host: "unix:///var/run/docker.sock"}, false},
		{"Windows deploy", "windows-amd64", dockerSetup{Mode: "deploy", Host: "http://127.0.0.1:2375"}, false},
		{"unknown mode", "linux-amd64", dockerSetup{Mode: "admin", Host: "unix:///var/run/docker.sock"}, false},
		{"socket traversal", "linux-amd64", dockerSetup{Mode: "read", Host: "unix:///run/../docker.sock"}, false},
		{"newline", "linux-amd64", dockerSetup{Mode: "read", Host: "unix:///run/docker.sock\nUser=root"}, false},
		{"root injection", "linux-amd64", dockerSetup{Mode: "deploy", Host: "unix:///var/run/docker.sock", ManagedRoot: "/srv/stacks\nUser=root"}, false},
		{"filesystem root", "linux-amd64", dockerSetup{Mode: "deploy", Host: "unix:///var/run/docker.sock", ManagedRoot: "/"}, false},
		{"unexpected root", "linux-amd64", dockerSetup{Mode: "read", Host: "unix:///var/run/docker.sock", ManagedRoot: "/srv/stacks"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.settings.validate(tc.target); (err == nil) != tc.valid {
				t.Fatalf("validate=%v, valid=%v", err, tc.valid)
			}
		})
	}
}

func TestDockerInstallerUpdatesOnlyExplicitDockerPermissions(t *testing.T) {
	var absent *dockerSetup
	features := []string{"restic.backup", "docker.manage", "docker.exec", "docker.deploy"}
	if absent.validate("linux-amd64") != nil || !slices.Equal(absent.enrollmentFeatures(features), features) {
		t.Fatal("omitted settings must preserve local configuration")
	}
	for _, mode := range []string{"read", "manage", "exec", "deploy"} {
		settings := &dockerSetup{Mode: mode}
		required, _ := protocol.DockerTierCapabilities(mode)
		want := append([]string{"restic.backup"}, required...)
		if got := settings.enrollmentFeatures(features); !slices.Equal(got, want) {
			t.Fatalf("mode=%s features=%v want=%v", mode, got, want)
		}
	}
}

func TestDockerOmittedRootRemainsHostChoice(t *testing.T) {
	settings := dockerSetup{Mode: "deploy", Host: "unix:///var/run/docker.sock"}
	if err := settings.validate("linux-amd64"); err != nil {
		t.Fatal(err)
	}
	if settings.ManagedRoot != "" {
		t.Fatal("server replaced omitted root before host could preserve local choice")
	}
}

func TestDockerMountedFileSettings(t *testing.T) {
	for _, tc := range []struct {
		target, mode, host, files string
		valid                     bool
	}{
		{"linux-amd64", "exec", "unix:///var/run/docker.sock", "read", true},
		{"linux-arm64", "deploy", "unix:///var/run/docker.sock", "write", true},
		{"linux-amd64", "exec", "unix:///var/run/docker.sock", "", true},
		{"windows-amd64", "read", "http://127.0.0.1:2375", "disabled", true},
		{"linux-amd64", "read", "unix:///var/run/docker.sock", "read", false},
		{"linux-amd64", "manage", "unix:///var/run/docker.sock", "write", false},
		{"linux-amd64", "exec", "http://127.0.0.1:2375", "read", false},
		{"windows-amd64", "exec", "http://127.0.0.1:2375", "write", false},
		{"linux-amd64", "exec", "unix:///var/run/docker.sock", "write\nUser=root", false},
	} {
		t.Run(tc.target+tc.mode+tc.host+tc.files, func(t *testing.T) {
			d := dockerSetup{Mode: tc.mode, Host: tc.host, Files: tc.files}
			if err := d.validate(tc.target); (err == nil) != tc.valid {
				t.Fatalf("validate=%v, valid=%v", err, tc.valid)
			}
		})
	}
}
