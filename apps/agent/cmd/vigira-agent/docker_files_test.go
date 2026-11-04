package main

import (
	"io"
	"log/slog"
	"runtime"
	"testing"

	agentchannel "github.com/deranjer/vigara/apps/agent/internal/channel"
	"github.com/deranjer/vigara/internal/protocol"
)

func TestMountFilesRequireLocalOptInAndExecTier(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, test := range []struct {
		mode, endpoint string
		exec, enabled  bool
	}{
		{"", "unix:///var/run/docker.sock", true, false},
		{"invalid", "unix:///var/run/docker.sock", true, false},
		{"read", "http://127.0.0.1:2375", true, false},
		{"read", "unix:///var/run/docker.sock", false, false},
		{"read", "unix:///var/run/docker.sock", true, runtime.GOOS == "linux"},
		{"write", "unix:///var/run/docker.sock", true, runtime.GOOS == "linux"},
	} {
		t.Run(test.mode+test.endpoint, func(t *testing.T) {
			t.Setenv("VIGIRA_DOCKER_ENABLED", "true")
			t.Setenv("VIGIRA_DOCKER_HOST", test.endpoint)
			t.Setenv("VIGIRA_DOCKER_FILES", test.mode)
			capabilities := func() []string {
				if test.exec {
					return []string{protocol.CapabilityDockerManage, protocol.CapabilityDockerExec}
				}
				return []string{protocol.CapabilityDockerManage}
			}
			live := agentchannel.NewClient("https://unused.example", "unused", "test", false, capabilities, logger)
			registerDocker(live, logger)
			for _, method := range []string{protocol.MethodDockerFilesList, protocol.MethodDockerFilesRead, protocol.MethodDockerFilesWrite} {
				if (live.Calls[method] != nil) != test.enabled {
					t.Fatalf("%s registered = %v, want %v", method, live.Calls[method] != nil, test.enabled)
				}
			}
		})
	}
}
