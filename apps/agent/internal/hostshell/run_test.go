package hostshell

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/deranjer/vigara/apps/agent/internal/channel"
	"github.com/deranjer/vigara/internal/protocol"
)

func TestEveryAllowedCommandHasFixedArguments(t *testing.T) {
	for _, name := range protocol.HostRunCommands {
		p := protocol.HostRunParams{Command: name}
		if name == protocol.HostCmdLS || name == protocol.HostCmdDU {
			p.Path = "/tmp"
		}
		program, args, err := argv(p)
		if err != nil || program == "" {
			t.Fatalf("%s: %v", name, err)
		}
		for _, arg := range args {
			if strings.ContainsAny(arg, ";|&$`") {
				t.Fatalf("%s: suspicious argument %q", name, arg)
			}
		}
	}
	_, args, _ := argv(protocol.HostRunParams{Command: "ls", Path: "/tmp"})
	if args[len(args)-2] != "--" || args[len(args)-1] != "/tmp" {
		t.Fatalf("path must follow --: %v", args)
	}
}

func TestHostRunRequiresOptInAndValidInput(t *testing.T) {
	var caps []string
	client := channel.NewClient("", "", "test", false, func() []string { return caps }, nil)
	Register(client)
	call := func(body string) error {
		_, err := client.Calls[protocol.MethodHostRun](context.Background(), json.RawMessage(body))
		return err
	}
	var channelErr *protocol.ChannelError
	if err := call(`{"command":"df"}`); !errors.As(err, &channelErr) || channelErr.Code != protocol.ErrCodeTierInsufficient {
		t.Fatalf("no opt-in = %v", err)
	}
	caps = []string{protocol.CapabilityHostExec}
	for _, body := range []string{`{"command":"cat","path":"/etc/shadow"}`, `{"command":"ls","path":"/etc/../root"}`, `{"command":"df","extra":1}`, `nope`} {
		if err := call(body); !errors.As(err, &channelErr) || channelErr.Code != protocol.ErrCodeBadRequest {
			t.Fatalf("%s = %v", body, err)
		}
	}
}

func TestHostRunReturnsBoundedOutput(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("host commands run on Linux")
	}
	result, err := runAllowed(context.Background(), protocol.HostRunParams{Command: "ls", Path: "/"})
	if err != nil || result.ExitCode != 0 || !strings.Contains(result.Stdout, "etc") {
		t.Fatalf("ls = %+v, %v", result, err)
	}
	result, err = runAllowed(context.Background(), protocol.HostRunParams{Command: "ls", Path: "/definitely-missing"})
	if err != nil || result.ExitCode == 0 || result.Stderr == "" {
		t.Fatalf("missing path = %+v, %v", result, err)
	}
}

func TestCapWriterTruncates(t *testing.T) {
	w := &capWriter{limit: 4}
	if n, _ := w.Write([]byte("abcdef")); n != 6 || w.buf.String() != "abcd" || !w.truncated {
		t.Fatalf("got %q truncated=%v", w.buf.String(), w.truncated)
	}
}
