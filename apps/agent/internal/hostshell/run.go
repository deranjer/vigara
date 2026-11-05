package hostshell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"runtime"
	"time"

	"github.com/deranjer/vigara/apps/agent/internal/channel"
	"github.com/deranjer/vigara/internal/protocol"
)

// registerRun serves the allowlisted read-only commands. Nothing here starts a shell
// or accepts free-form arguments: the argument vector comes from argv below.
func registerRun(c *channel.Client) {
	c.Calls[protocol.MethodHostRun] = func(ctx context.Context, raw json.RawMessage) (any, error) {
		if err := protocol.CheckHostExec(protocol.MethodHostExec, c.Capabilities()); err != nil {
			return nil, err
		}
		var p protocol.HostRunParams
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&p) != nil {
			return nil, &protocol.ChannelError{Code: protocol.ErrCodeBadRequest, Message: "Invalid command request."}
		}
		if err := p.Normalize(); err != nil {
			return nil, &protocol.ChannelError{Code: protocol.ErrCodeBadRequest, Message: err.Error()}
		}
		if runtime.GOOS != "linux" {
			return nil, &protocol.ChannelError{Code: protocol.ErrCodeBadRequest, Message: "Read-only host commands are available on Linux devices."}
		}
		return runAllowed(ctx, p)
	}
}

// argv maps an already-validated request to a program and fixed arguments.
func argv(p protocol.HostRunParams) (string, []string, error) {
	switch p.Command {
	case protocol.HostCmdDF:
		return "df", []string{"-hT", "-x", "tmpfs", "-x", "devtmpfs", "-x", "overlay"}, nil
	case protocol.HostCmdFree:
		return "free", []string{"-h"}, nil
	case protocol.HostCmdUptime:
		return "uptime", nil, nil
	case protocol.HostCmdUname:
		return "uname", []string{"-a"}, nil
	case protocol.HostCmdLS:
		return "ls", []string{"-la", "--time-style=long-iso", "--", p.Path}, nil
	case protocol.HostCmdDU:
		return "du", []string{"-xh", "--max-depth=1", "--", p.Path}, nil
	case protocol.HostCmdDockerPS:
		return "docker", []string{"ps", "-a", "--no-trunc"}, nil
	case protocol.HostCmdDockerImages:
		return "docker", []string{"images"}, nil
	case protocol.HostCmdDockerNetworks:
		return "docker", []string{"network", "ls"}, nil
	case protocol.HostCmdDockerVolumes:
		return "docker", []string{"volume", "ls"}, nil
	}
	return "", nil, errors.New("unknown command")
}

// capWriter keeps the first limit bytes and remembers that more was written.
type capWriter struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (w *capWriter) Write(data []byte) (int, error) {
	if room := w.limit - w.buf.Len(); room < len(data) {
		w.truncated = true
		if room > 0 {
			w.buf.Write(data[:room])
		}
		return len(data), nil
	}
	return w.buf.Write(data)
}

func runAllowed(parent context.Context, p protocol.HostRunParams) (protocol.HostRunResult, error) {
	name, args, err := argv(p)
	if err != nil {
		return protocol.HostRunResult{}, &protocol.ChannelError{Code: protocol.ErrCodeBadRequest, Message: err.Error()}
	}
	program, err := exec.LookPath(name)
	if err != nil {
		return protocol.HostRunResult{}, &protocol.ChannelError{Code: protocol.ErrCodeExecFailed, Message: name + " is not installed on this device."}
	}
	ctx, cancel := context.WithTimeout(parent, protocol.HostRunTimeoutSeconds*time.Second)
	defer cancel()
	stdout := &capWriter{limit: protocol.HostRunMaxOutputBytes}
	stderr := &capWriter{limit: 8 << 10}
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin", "LC_ALL=C"}
	cmd.WaitDelay = time.Second
	runErr := cmd.Run()
	result := protocol.HostRunResult{Stdout: stdout.buf.String(), Stderr: stderr.buf.String(), Truncated: stdout.truncated || stderr.truncated}
	var exit *exec.ExitError
	switch {
	case runErr == nil:
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		result.TimedOut, result.ExitCode = true, -1
	case errors.As(runErr, &exit):
		result.ExitCode = exit.ExitCode()
	default:
		return protocol.HostRunResult{}, &protocol.ChannelError{Code: protocol.ErrCodeExecFailed, Message: "Could not run the command: " + runErr.Error()}
	}
	return result, nil
}
