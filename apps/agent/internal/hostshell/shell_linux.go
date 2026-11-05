package hostshell

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"github.com/creack/pty"
	"github.com/deranjer/vigara/internal/protocol"
)

type linuxTerminal struct {
	*os.File
	cmd  *exec.Cmd
	once sync.Once
}

func start(p protocol.HostExecParams) (terminal, error) {
	shell := "/bin/" + p.Shell
	if p.Shell == "powershell" {
		return nil, errors.New("PowerShell is available on Windows devices")
	}
	if p.Shell == protocol.ShellAuto {
		shell = "/bin/bash"
		if _, err := os.Stat(shell); err != nil {
			shell = "/bin/sh"
		}
	}
	cmd := exec.Command(shell, "-i")
	// Do not expose the agent's service environment (credentials and keys) to shells.
	cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "TERM=xterm-256color", "COLORTERM=truecolor"}
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
		cmd.Env = append(cmd.Env, "HOME="+home)
	}
	file, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: p.Cols, Rows: p.Rows})
	if err != nil {
		return nil, err
	}
	return &linuxTerminal{File: file, cmd: cmd}, nil
}

func (t *linuxTerminal) Resize(cols, rows uint16) error {
	return pty.Setsize(t.File, &pty.Winsize{Cols: cols, Rows: rows})
}
func (t *linuxTerminal) Close() error {
	t.once.Do(func() {
		// The PTY starts a new session. Kill the shell's process group as well as closing its terminal.
		_ = syscall.Kill(-t.cmd.Process.Pid, syscall.SIGKILL)
		_ = t.File.Close()
	})
	return nil
}
func (t *linuxTerminal) Wait(ctx context.Context) (int, error) {
	stop := context.AfterFunc(ctx, func() { _ = t.Close() })
	defer stop()
	err := t.cmd.Wait()
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	return 0, err
}
