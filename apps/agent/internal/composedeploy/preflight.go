package composedeploy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/deranjer/vigara/internal/placement"
)

var errRequirements = errors.New("profile requirements are unavailable, changed, or unsupported on this device")

// exactPath rejects symlinks in every component, including an optional missing path's parents.
func exactPath(path string) (os.FileInfo, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errRequirements
	}
	current := filepath.VolumeName(path) + string(os.PathSeparator)
	parts := strings.Split(strings.TrimPrefix(path, current), string(os.PathSeparator))
	var info os.FileInfo
	var err error
	if len(parts) == 1 && parts[0] == "" {
		return os.Lstat(current)
	}
	for _, part := range parts {
		current = filepath.Join(current, part)
		info, err = os.Lstat(current)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, errRequirements
		}
	}
	return info, nil
}

// mountedExactly requires a real mount boundary, not a directory on its parent's filesystem.
func mountedExactly(data []byte, path string) bool {
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 6 && unescape.Replace(fields[4]) == path && strings.Contains(line, " - ") {
			return true
		}
	}
	return false
}

func (r *Runner) preflight(ctx context.Context, dir string, p placement.Profile) error {
	if p.ID == "" {
		return nil
	}
	if r.DockerHost != "" && r.DockerHost != "unix:///var/run/docker.sock" {
		return errRequirements
	}
	for key, raw := range p.Requirements {
		switch key {
		case "startupDependencies":
			items, ok := raw.([]any)
			if !ok || len(items) != 0 {
				return errRequirements
			}
		case "capabilities":
			items, ok := raw.(map[string]any)
			if !ok || len(items) != 0 {
				return errRequirements
			}
		case "mounts", "devices":
			items, ok := raw.([]any)
			if !ok {
				return errRequirements
			}
			for _, raw := range items {
				item, ok := raw.(map[string]any)
				if !ok {
					return errRequirements
				}
				path, ok := item["hostPath"].(string)
				if !ok {
					return errRequirements
				}
				info, err := exactPath(path)
				required, ok := item["required"].(bool)
				if !ok {
					return errRequirements
				}
				if os.IsNotExist(err) && !required {
					continue
				}
				if err != nil {
					return errRequirements
				}
				if key == "devices" && info.Mode()&os.ModeDevice == 0 {
					return errRequirements
				}
				if mounted, _ := item["requireMountedFilesystem"].(bool); mounted {
					data, err := os.ReadFile("/proc/self/mountinfo")
					if err != nil || !mountedExactly(data, path) {
						return errRequirements
					}
				}
			}
		case "networks", "volumes":
			items, ok := raw.([]any)
			if !ok {
				return errRequirements
			}
			for _, raw := range items {
				item, ok := raw.(map[string]any)
				if !ok {
					return errRequirements
				}
				name, ok := item["name"].(string)
				if !ok || name == "" || strings.HasPrefix(name, "-") {
					return errRequirements
				}
				external, ok := item["external"].(bool)
				if !ok || !external {
					return errRequirements
				}
				if managed, _ := item["managed"].(bool); managed {
					return errRequirements
				}
				kind := "network"
				if key == "volumes" {
					kind = "volume"
				}
				data, err := r.command(ctx, dir, kind, "inspect", "--format", "{{json .}}", name)
				var actual map[string]any
				if err != nil || json.Unmarshal(data, &actual) != nil || actual["Name"] != name {
					return errRequirements
				}
				for _, attribute := range []string{"driver", "scope", "internal", "attachable"} {
					if expected, exists := item[attribute]; exists {
						field := strings.ToUpper(attribute[:1]) + attribute[1:]
						if actual[field] != expected {
							return errRequirements
						}
					}
				}
			}
		default:
			return errRequirements
		}
	}
	return nil
}
