package protocol

import (
	"errors"
	"slices"
	"strings"
)

const MethodHostExec = "host.exec"
const CapabilityHostExec = "host.exec"

func init() { channelMethodTiers[MethodHostExec] = "" }

func CheckHostExec(method string, capabilities []string) error {
	if method == MethodHostExec && !slices.Contains(capabilities, CapabilityHostExec) {
		return &ChannelError{Code: ErrCodeTierInsufficient, Message: "Device shells are not enabled. Reinstall the agent with device shell access enabled."}
	}
	return nil
}

type HostExecParams struct {
	Shell string `json:"shell,omitempty"`
	Cols  uint16 `json:"cols,omitempty"`
	Rows  uint16 `json:"rows,omitempty"`
}

func (p *HostExecParams) Normalize() error {
	p.Shell = strings.ToLower(strings.TrimSpace(p.Shell))
	if p.Shell == "" {
		p.Shell = ShellAuto
	}
	switch p.Shell {
	case ShellAuto, ShellSh, ShellBash, "powershell":
	default:
		return errors.New("shell must be auto, sh, bash, or powershell")
	}
	p.Cols = ClampExecSize(p.Cols, DefaultExecCols)
	p.Rows = ClampExecSize(p.Rows, DefaultExecRows)
	return nil
}
