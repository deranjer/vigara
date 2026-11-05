package agent

import (
	"errors"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/deranjer/vigara/internal/protocol"
)

// Docker settings are an explicit host installation choice, independent of
// the server's enrollment ceiling. An omitted choice preserves host settings.
type dockerSetup struct {
	Mode        string `json:"mode"`
	Host        string `json:"host"`
	ManagedRoot string `json:"managedRoot,omitempty"`
	StackOwner  string `json:"stackOwner,omitempty"`
	Files       string `json:"files,omitempty"`
}

var dockerSocketPath = regexp.MustCompile(`^/[a-zA-Z0-9_./-]+$`)
var dockerManagedPath = regexp.MustCompile(`^/[a-zA-Z0-9_./ -]+$`)
var dockerStackOwner = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,63}(:[A-Za-z0-9_][A-Za-z0-9_.-]{0,63})?$`)

func (d *dockerSetup) validate(target string) error {
	if d == nil {
		return nil
	}
	switch d.Mode {
	case protocol.DockerModeRead, protocol.DockerModeManage, protocol.DockerModeExec, protocol.DockerModeDeploy:
	default:
		return errors.New("Docker access must be read, manage, exec, or deploy")
	}
	if strings.HasPrefix(d.Host, "unix://") {
		path := strings.TrimPrefix(d.Host, "unix://")
		if !strings.HasPrefix(target, "linux-") || !dockerSocketPath.MatchString(path) || strings.Contains(path, "..") {
			return errors.New("Linux Docker sockets require an absolute path without parent traversal")
		}
	} else {
		u, err := url.Parse(d.Host)
		if err != nil || u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("Docker endpoint must be a Linux unix socket or a loopback HTTP IP and port")
		}
		ip := net.ParseIP(u.Hostname())
		port, err := strconv.Atoi(u.Port())
		if ip == nil || !ip.IsLoopback() || err != nil || port < 1 || port > 65535 {
			return errors.New("Docker HTTP endpoint must use a loopback IP and port")
		}
	}
	switch d.Files {
	case "", "disabled":
	case "read", "write":
		if !strings.HasPrefix(target, "linux-") || !strings.HasPrefix(d.Host, "unix://") || (d.Mode != protocol.DockerModeExec && d.Mode != protocol.DockerModeDeploy) {
			return errors.New("Mounted file access requires a native Linux host, local Docker socket, and exec or deploy access")
		}
	default:
		return errors.New("Mounted file access must be disabled, read, or write")
	}
	if d.Mode == protocol.DockerModeDeploy {
		if d.StackOwner != "" && !dockerStackOwner.MatchString(d.StackOwner) {
			return errors.New("Stack owner must be a local user or UID, optionally followed by :group or :GID")
		}
		if !strings.HasPrefix(target, "linux-") {
			return errors.New("Compose stack deployment is supported on Linux agents only")
		}
		// An omitted root is resolved on the host: preserve its local choice on
		// reinstall, or use the fresh-install default when none exists.
		if d.ManagedRoot != "" && (!dockerManagedPath.MatchString(d.ManagedRoot) || strings.Contains(d.ManagedRoot, "..") || strings.Trim(d.ManagedRoot, "/ ") == "") {
			return errors.New("Compose root must be an absolute Linux directory without parent traversal")
		}
	} else if d.ManagedRoot != "" {
		return errors.New("A Compose root requires deploy access")
	} else if d.StackOwner != "" {
		return errors.New("A stack owner requires deploy access")
	}
	return nil
}

// A selected Docker installation explicitly replaces only the Docker ceiling.
// Other enrollment permissions survive reinstall; omission preserves everything.
func (d *dockerSetup) enrollmentFeatures(features []string) []string {
	if d == nil {
		return features
	}
	result := []string{}
	for _, feature := range features {
		if !strings.HasPrefix(feature, "docker.") {
			result = append(result, feature)
		}
	}
	required, _ := protocol.DockerTierCapabilities(d.Mode)
	return append(result, required...)
}
