//go:build linux || windows

package hostshell

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deranjer/vigara/apps/agent/internal/channel"
	"github.com/deranjer/vigara/internal/protocol"
)

func TestShellRelaysRealTerminalAndReportsExit(t *testing.T) {
	client := channel.NewClient("", "", "test", false, func() []string { return []string{protocol.CapabilityHostExec} }, nil)
	Register(client)
	input := make(chan []byte, 1)
	command := "printf 'remote-'; printf 'shell-ok\\n'; exit 7\r"
	if runtime.GOOS == "windows" {
		command = "Write-Output ('remote-' + 'shell-ok'); exit 7\r"
	}
	input <- []byte(command)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var mu sync.Mutex
	var output strings.Builder
	err := client.Interactive[protocol.MethodHostExec](ctx, json.RawMessage(`{}`), &channel.StreamIO{Input: input, Send: func(data []byte) error { mu.Lock(); defer mu.Unlock(); output.Write(data); return nil }})
	var end *protocol.ChannelError
	if !errors.As(err, &end) || end.Code != protocol.ErrCodeExecExit || !strings.Contains(end.Message, "7") {
		t.Fatalf("exit = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(output.String(), "remote-shell-ok") {
		t.Fatalf("missing shell output: %q", output.String())
	}
}

func TestShellCancellationStopsTerminal(t *testing.T) {
	client := channel.NewClient("", "", "test", false, func() []string { return []string{protocol.CapabilityHostExec} }, nil)
	Register(client)
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := client.Interactive[protocol.MethodHostExec](ctx, json.RawMessage(`{}`), &channel.StreamIO{Send: func([]byte) error { return nil }})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancel = %v", err)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("terminal cleanup stalled")
	}
}

func TestShellIsRefusedWithoutLocalOptIn(t *testing.T) {
	client := channel.NewClient("", "", "test", false, func() []string { return []string{protocol.CapabilityDockerExec} }, nil)
	Register(client)
	err := client.Interactive[protocol.MethodHostExec](context.Background(), json.RawMessage(`{}`), &channel.StreamIO{})
	var end *protocol.ChannelError
	if !errors.As(err, &end) || end.Code != protocol.ErrCodeTierInsufficient {
		t.Fatalf("no opt-in = %v", err)
	}
}
