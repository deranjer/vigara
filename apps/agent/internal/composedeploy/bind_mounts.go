package composedeploy

import (
	"fmt"
	"os"
)

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
