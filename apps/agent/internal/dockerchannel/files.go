package dockerchannel

import (
	"context"
	"encoding/json"

	"github.com/deranjer/vigara/apps/agent/internal/channel"
	"github.com/deranjer/vigara/apps/agent/internal/mountfiles"
	"github.com/deranjer/vigara/internal/protocol"
)

// RegisterFiles is called only after local filesystem opt-in. Both endpoints
// independently enforce the enrollment's exec tier before invoking these calls.
func RegisterFiles(c *channel.Client, files *mountfiles.Service) {
	for method, operation := range map[string]func(context.Context, protocol.DockerFileParams) (any, error){
		protocol.MethodDockerFilesList:  func(ctx context.Context, p protocol.DockerFileParams) (any, error) { return files.List(ctx, p) },
		protocol.MethodDockerFilesRead:  func(ctx context.Context, p protocol.DockerFileParams) (any, error) { return files.Read(ctx, p) },
		protocol.MethodDockerFilesWrite: func(ctx context.Context, p protocol.DockerFileParams) (any, error) { return files.Write(ctx, p) },
	} {
		c.Calls[method] = func(ctx context.Context, raw json.RawMessage) (any, error) {
			var p protocol.DockerFileParams
			if err := decode(raw, &p); err != nil {
				return nil, err
			}
			result, err := operation(ctx, p)
			return result, mapResourceError(err)
		}
	}
}
