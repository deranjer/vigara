package hostshell

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/UserExistsError/conpty"
	"github.com/deranjer/vigara/internal/protocol"
	"golang.org/x/sys/windows"
)

type windowsTerminal struct {
	*conpty.ConPty
	once sync.Once
}

func start(p protocol.HostExecParams) (terminal, error) {
	if p.Shell != protocol.ShellAuto && p.Shell != "powershell" {
		return nil, errors.New("select PowerShell for a Windows device")
	}
	shell, err := exec.LookPath("pwsh.exe")
	if err != nil {
		shell = filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	}
	env := []string{}
	for _, key := range []string{"SystemRoot", "WINDIR", "PATH", "PATHEXT", "TEMP", "TMP", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "ProgramFiles", "ProgramFiles(x86)", "ProgramData"} {
		if value := os.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	term, err := conpty.Start(windows.EscapeArg(shell)+" -NoLogo -NoProfile", conpty.ConPtyDimensions(int(p.Cols), int(p.Rows)), conpty.ConPtyEnv(env))
	if err != nil {
		return nil, err
	}
	return &windowsTerminal{ConPty: term}, nil
}
func (t *windowsTerminal) Resize(cols, rows uint16) error {
	return t.ConPty.Resize(int(cols), int(rows))
}
func (t *windowsTerminal) Close() error {
	var err error
	t.once.Do(func() { err = t.ConPty.Close() })
	return err
}
func (t *windowsTerminal) Wait(ctx context.Context) (int, error) {
	code, err := t.ConPty.Wait(ctx)
	return int(code), err
}
