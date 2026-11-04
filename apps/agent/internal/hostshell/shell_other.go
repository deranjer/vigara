//go:build !linux && !windows

package hostshell

import (
	"errors"
	"github.com/deranjer/vigara/internal/protocol"
)

func start(protocol.HostExecParams) (terminal, error) {
	return nil, errors.New("device shells require Linux or Windows")
}
