package main

import (
	"log/slog"
	"path/filepath"
	"testing"

	agentruntime "github.com/deranjer/vigara/apps/agent/internal/runtime"
	"github.com/deranjer/vigara/internal/protocol"
	"slices"
)

func TestConfigureShellPreservesEnrollmentAndControlsCapabilities(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	state := agentruntime.State{AgentID: "existing", Credential: "private", ServerURL: "https://example.invalid"}
	if err := agentruntime.Save(path, state); err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{true, false} {
		value := "false"
		if enabled {
			value = "true"
		}
		if err := execute([]string{"--config", path, "configure-shell", value}, slog.Default()); err != nil {
			t.Fatal(err)
		}
		client, err := agentruntime.NewClient(path)
		if err != nil {
			t.Fatal(err)
		}
		if client.State.AgentID != state.AgentID || client.State.Credential != state.Credential || client.State.ServerURL != state.ServerURL {
			t.Fatal("shell configuration changed enrollment")
		}
		if slices.Contains(client.Capabilities(), protocol.CapabilityHostExec) != enabled {
			t.Fatalf("shell %v capabilities = %v", enabled, client.Capabilities())
		}
	}
	if err := execute([]string{"--config", path, "configure-shell", "invalid"}, slog.Default()); err == nil {
		t.Fatal("accepted invalid shell setting")
	}
}
