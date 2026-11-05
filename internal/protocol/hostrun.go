package protocol

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
)

// MethodHostRun runs one command from a fixed read-only allowlist on a device. It is
// not a shell: the agent builds the argument vector itself from the command name and
// a validated path, so no caller text ever reaches a shell or an arbitrary program.
// It needs the same host.exec capability as the interactive device shell.
const MethodHostRun = "host.run"

func init() { channelMethodTiers[MethodHostRun] = "" }

// Bounds applied by the agent to every run.
const (
	HostRunTimeoutSeconds = 20
	HostRunMaxOutputBytes = 64 << 10
)

// Allowlisted command names.
const (
	HostCmdDF             = "df"
	HostCmdFree           = "free"
	HostCmdUptime         = "uptime"
	HostCmdUname          = "uname"
	HostCmdLS             = "ls"
	HostCmdDU             = "du"
	HostCmdDockerPS       = "docker_ps"
	HostCmdDockerImages   = "docker_images"
	HostCmdDockerNetworks = "docker_networks"
	HostCmdDockerVolumes  = "docker_volumes"
)

// HostRunCommands lists the allowed names; those in hostRunPathCommands take one path.
var HostRunCommands = []string{HostCmdDF, HostCmdFree, HostCmdUptime, HostCmdUname, HostCmdLS, HostCmdDU,
	HostCmdDockerPS, HostCmdDockerImages, HostCmdDockerNetworks, HostCmdDockerVolumes}

var hostRunPathCommands = []string{HostCmdLS, HostCmdDU}

var hostRunPath = regexp.MustCompile(`^/[A-Za-z0-9_./@+:= -]{0,999}$`)

type HostRunParams struct {
	Command string `json:"command"`
	// Path is required for ls and du, and refused for everything else.
	Path string `json:"path,omitempty"`
}

// Normalize validates the request. Paths must be absolute and already clean, so
// "..", doubled slashes and trailing slashes are refused rather than rewritten.
func (p *HostRunParams) Normalize() error {
	p.Command = strings.ToLower(strings.TrimSpace(p.Command))
	if !slices.Contains(HostRunCommands, p.Command) {
		return fmt.Errorf("command must be one of: %s", strings.Join(HostRunCommands, ", "))
	}
	needsPath := slices.Contains(hostRunPathCommands, p.Command)
	if !needsPath {
		if p.Path != "" {
			return errors.New("this command does not take a path")
		}
		return nil
	}
	if !hostRunPath.MatchString(p.Path) || path.Clean(p.Path) != p.Path {
		return errors.New("path must be an absolute, clean path without special characters")
	}
	return nil
}

type HostRunResult struct {
	ExitCode  int    `json:"exitCode"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	TimedOut  bool   `json:"timedOut,omitempty"`
}
