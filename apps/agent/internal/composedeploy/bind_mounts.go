package composedeploy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/deranjer/vigara/internal/protocol"
)

type composeBindVolume struct {
	Type, Source, Target string
	Bind                 *struct {
		CreateHostPath *bool `json:"create_host_path"`
	} `json:"bind"`
}

var errBindCreationEncoding = &protocol.ChannelError{
	Code:    protocol.ErrCodeInternal,
	Message: "Could not determine this device's Compose bind directory creation settings. Check Docker Compose availability and retry.",
}

// Compose versions differ in which boolean is omitted from normalized JSON:
// older versions omit false; newer versions omit true. Probe the installed
// serializer with explicit values instead of guessing from a missing field.
// This only runs `config` on fixed, non-secret inputs, never creates containers
// or host directories, and is needed only for an omitted flag on a missing path.
func (r *Runner) omittedBindCreation(ctx context.Context, dir string) (bool, error) {
	f, err := os.CreateTemp(dir, ".vigara-bind-encoding-*.yaml")
	if err != nil {
		return false, errBindCreationEncoding
	}
	defer os.Remove(f.Name())
	_, writeErr := f.WriteString(`services:
  create:
    image: busybox
    volumes:
      - type: bind
        source: ./.vigara-bind-probe
        target: /probe
        bind:
          create_host_path: true
  require:
    image: busybox
    volumes:
      - type: bind
        source: ./.vigara-bind-probe
        target: /probe
        bind:
          create_host_path: false
`)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		return false, errBindCreationEncoding
	}
	b, err := r.command(ctx, dir, "compose", "-p", "vigara-bind-encoding", "--project-directory", dir, "-f", f.Name(), "config", "--format", "json")
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, errBindCreationEncoding
	}
	var model struct {
		Services map[string]struct {
			Volumes []composeBindVolume `json:"volumes"`
		} `json:"services"`
	}
	if json.Unmarshal(b, &model) != nil {
		return false, errBindCreationEncoding
	}
	on, off := model.Services["create"].Volumes, model.Services["require"].Volumes
	if len(on) != 1 || len(off) != 1 || on[0].Bind == nil || off[0].Bind == nil {
		return false, errBindCreationEncoding
	}
	trueFlag, falseFlag := on[0].Bind.CreateHostPath, off[0].Bind.CreateHostPath
	switch {
	case trueFlag == nil && falseFlag != nil && !*falseFlag:
		return true, nil
	case trueFlag != nil && *trueFlag && (falseFlag == nil || !*falseFlag):
		return false, nil
	default:
		return false, errBindCreationEncoding
	}
}

// Only resolved mount metadata and fixed reasons cross the channel, never the
// underlying OS error or command output, which may contain unrelated secrets.
type bindMountError struct {
	Service, Source, Target string
	Reason                  string
}

func (e *bindMountError) Error() string {
	return fmt.Sprintf("Service %q bind mount %q -> %q: %s", e.Service, e.Source, e.Target, e.Reason)
}

func (e *bindMountError) Unwrap() error { return errBindMount }

func newBindMountError(service, source, target string, cause error) *bindMountError {
	reason := "the agent could not inspect the host path. Check the path and its parent directories on this device."
	switch {
	case os.IsNotExist(cause):
		reason = "the host path does not exist and Compose has not enabled automatic directory creation for this mount. Create the path on this device, use short volume syntax, or set bind.create_host_path: true for a directory mount."
	case os.IsPermission(cause):
		reason = "the agent cannot access the host path. Check permissions on this path and its parent directories on this device."
	}
	return &bindMountError{Service: service, Source: source, Target: target, Reason: reason}
}
