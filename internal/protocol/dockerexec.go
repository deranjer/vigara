package protocol

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

// MethodDockerExec opens an interactive terminal inside a running container. It is
// a two-way stream: the agent sends terminal output as stream chunks, and the
// server sends keystrokes as FrameStreamInput and resizes as FrameStreamControl.
// It needs the exec tier.
const MethodDockerExec = "docker.exec"

func init() { channelMethodTiers[MethodDockerExec] = DockerModeExec }

// ErrCodeExecExit ends a terminal whose shell exited with a non-zero status; the
// message gives the status. A clean exit ends the stream without an error.
const ErrCodeExecExit = "exec_exit"

// ErrCodeExecFailed reports that Docker could not start the terminal, for example
// because the image has no shell. The message is Docker's own explanation.
const ErrCodeExecFailed = "exec_failed"

// Terminal limits.
const (
	ExecIdleTimeout  = 15 * time.Minute // no keystrokes for this long closes the terminal
	ExecMaxDuration  = 4 * time.Hour    // no terminal lives longer than this
	MaxExecInput     = 64 << 10         // largest single keystroke or paste chunk
	DefaultExecCols  = 80
	DefaultExecRows  = 24
	MinExecSize      = 10
	MaxExecSize      = 500
	execDefaultShell = "auto"
)

// Shells a terminal may start. "auto" uses bash when the image has it and sh otherwise.
const (
	ShellAuto = "auto"
	ShellSh   = "sh"
	ShellBash = "bash"
)

var execUser = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}(:[A-Za-z0-9_.-]{1,64})?$`)

// DockerExecParams starts a terminal. Zero Cols and Rows use the defaults.
type DockerExecParams struct {
	ID    string `json:"id"`
	Shell string `json:"shell,omitempty"`
	// User overrides the container's user (name or numeric ID, optionally with a group).
	User string `json:"user,omitempty"`
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
}

// ExecControl is the body of a FrameStreamControl for a terminal.
type ExecControl struct {
	Type string `json:"type"`
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
}

// ControlResize asks the terminal to change size.
const ControlResize = "resize"

// ClampExecSize forces a terminal dimension into the supported range.
func ClampExecSize(value uint16, fallback uint16) uint16 {
	if value == 0 {
		return fallback
	}
	return min(max(value, MinExecSize), MaxExecSize)
}

// Normalize applies defaults and rejects values a terminal must not be started with.
func (p *DockerExecParams) Normalize() error {
	p.Shell = strings.ToLower(strings.TrimSpace(p.Shell))
	switch p.Shell {
	case "":
		p.Shell = execDefaultShell
	case ShellAuto, ShellSh, ShellBash:
	default:
		return errors.New("shell must be auto, sh, or bash")
	}
	p.User = strings.TrimSpace(p.User)
	if p.User != "" && !execUser.MatchString(p.User) {
		return errors.New("user must be a name or numeric ID, optionally followed by :group")
	}
	p.Cols = ClampExecSize(p.Cols, DefaultExecCols)
	p.Rows = ClampExecSize(p.Rows, DefaultExecRows)
	return nil
}
