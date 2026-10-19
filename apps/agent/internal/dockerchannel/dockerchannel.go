// Package dockerchannel exposes the read-only Docker client as live channel
// methods. It is registered only when local Docker collection is enabled, so a
// host that has not opted in answers every Docker method as unknown.
package dockerchannel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/deranjer/vigara/apps/agent/internal/channel"
	"github.com/deranjer/vigara/apps/agent/internal/dockerapi"
	"github.com/deranjer/vigara/internal/protocol"
)

// Register adds the Docker read methods to the channel client.
func Register(c *channel.Client, api *dockerapi.Client) {
	c.Calls[protocol.MethodDockerList] = func(ctx context.Context, _ json.RawMessage) (any, error) {
		result, err := api.List(ctx)
		return result, mapError(err)
	}
	c.Calls[protocol.MethodDockerInspect] = func(ctx context.Context, raw json.RawMessage) (any, error) {
		var params protocol.DockerInspectParams
		if err := decode(raw, &params); err != nil {
			return nil, err
		}
		result, err := api.Inspect(ctx, params.ID, params.RevealEnv)
		return result, mapError(err)
	}
	c.Streams[protocol.MethodDockerLogs] = func(ctx context.Context, raw json.RawMessage, send func([]byte) error) error {
		var params protocol.DockerLogsParams
		if err := decode(raw, &params); err != nil {
			return err
		}
		return mapError(api.Logs(ctx, params, send))
	}
	c.Streams[protocol.MethodDockerStats] = func(ctx context.Context, raw json.RawMessage, send func([]byte) error) error {
		var params protocol.DockerStatsParams
		if err := decode(raw, &params); err != nil {
			return err
		}
		return mapError(api.Stats(ctx, params, send))
	}
}

// RegisterManage adds the methods that change the host. Call it only when the
// local operator enabled the manage tier; the channel also refuses these methods
// below that tier, so a host that opted out can never run them.
func RegisterManage(c *channel.Client, api *dockerapi.Client) {
	c.Calls[protocol.MethodDockerAction] = func(ctx context.Context, raw json.RawMessage) (any, error) {
		var params protocol.DockerActionParams
		if err := decode(raw, &params); err != nil {
			return nil, err
		}
		result, err := api.Action(ctx, params)
		return result, mapError(err)
	}
	c.Streams[protocol.MethodDockerRecreate] = func(ctx context.Context, raw json.RawMessage, send func([]byte) error) error {
		var params protocol.DockerRecreateParams
		if err := decode(raw, &params); err != nil {
			return err
		}
		// Progress is best effort: once the swap starts it completes even if the
		// browser has gone, so a failed send must not stop the work.
		_, err := api.Recreate(ctx, params, func(event protocol.DockerRecreateEvent) {
			if line, marshalErr := json.Marshal(event); marshalErr == nil {
				_ = send(append(line, '\n'))
			}
		})
		return mapError(err)
	}
}

func decode(raw json.RawMessage, into any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return &protocol.ChannelError{Code: protocol.ErrCodeBadRequest, Message: "the request parameters are invalid"}
	}
	return nil
}

// mapError turns Docker client failures into structured errors. Anything not
// recognised stays an ordinary error, which the channel reports generically so
// host details never reach the server.
func mapError(err error) error {
	var network net.Error
	var opError *net.OpError
	var apiErr *dockerapi.APIError
	var recreateErr *dockerapi.RecreateError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &recreateErr):
		return &protocol.ChannelError{Code: protocol.ErrCodeRecreateFailed, Message: clip(recreateErr.Error(), 400)}
	case errors.Is(err, dockerapi.ErrNotFound):
		return &protocol.ChannelError{Code: protocol.ErrCodeNotFound, Message: "that container no longer exists"}
	case errors.Is(err, dockerapi.ErrBadRequest):
		return &protocol.ChannelError{Code: protocol.ErrCodeBadRequest, Message: "the container reference is invalid"}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusConflict:
		message := apiErr.Message
		if message == "" {
			message = "Docker refused the action in the container's current state"
		}
		return &protocol.ChannelError{Code: protocol.ErrCodeConflict, Message: clip(message, 300)}
	case errors.As(err, &opError), errors.As(err, &network):
		return &protocol.ChannelError{Code: protocol.ErrCodeDockerDown, Message: "Docker could not be reached on this host"}
	}
	return err
}

func clip(value string, limit int) string {
	if len(value) > limit {
		return value[:limit]
	}
	return value
}

// RegisterExec adds the interactive terminal. Call it only when the local operator
// enabled the exec tier; the channel also refuses the method below that tier.
func RegisterExec(c *channel.Client, api *dockerapi.Client) {
	c.Interactive[protocol.MethodDockerExec] = func(ctx context.Context, raw json.RawMessage, stream *channel.StreamIO) error {
		var params protocol.DockerExecParams
		if err := decode(raw, &params); err != nil {
			return err
		}
		session, err := api.StartExec(ctx, params)
		if err != nil {
			return mapExecError(err)
		}
		defer session.Close()

		// Terminal output flows to the server until the shell closes its side.
		finished := make(chan error, 1)
		go func() {
			buffer := make([]byte, 16<<10)
			for {
				n, readErr := session.Read(buffer)
				if n > 0 {
					if sendErr := stream.Send(buffer[:n]); sendErr != nil {
						finished <- sendErr
						return
					}
				}
				if readErr != nil {
					if errors.Is(readErr, io.EOF) || errors.Is(readErr, net.ErrClosed) {
						readErr = nil
					}
					finished <- readErr
					return
				}
			}
		}()

		input, control := stream.Input, stream.Control
		for {
			select {
			case data, open := <-input:
				if !open {
					return nil // the server cancelled the stream
				}
				// A failed write means the shell is already gone; the output side reports the end.
				_, _ = session.Write(data)
			case message, open := <-control:
				if !open {
					control = nil
					continue
				}
				var request protocol.ExecControl
				if json.Unmarshal(message, &request) == nil && request.Type == protocol.ControlResize {
					_ = session.Resize(ctx, request.Cols, request.Rows)
				}
			case err := <-finished:
				if err != nil {
					return err
				}
				code, codeErr := session.ExitCode(context.WithoutCancel(ctx))
				switch {
				case codeErr != nil || code == 0:
					return nil
				case code == 126 || code == 127:
					return &protocol.ChannelError{Code: protocol.ErrCodeExecExit, Message: fmt.Sprintf("The shell could not be started in this container (status %d). The image may not include a shell.", code)}
				}
				return &protocol.ChannelError{Code: protocol.ErrCodeExecExit, Message: fmt.Sprintf("The shell exited with status %d.", code)}
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

// mapExecError is mapError, but keeps Docker's explanation when it cannot start a
// terminal, since "no such file" for the shell is exactly what the user needs to see.
func mapExecError(err error) error {
	var apiErr *dockerapi.APIError
	if errors.As(err, &apiErr) && apiErr.Status != http.StatusNotFound && apiErr.Status != http.StatusConflict {
		message := apiErr.Message
		if message == "" {
			message = "Docker could not start a terminal in this container"
		}
		return &protocol.ChannelError{Code: protocol.ErrCodeExecFailed, Message: clip(message, 300)}
	}
	return mapError(err)
}

// RegisterResources adds the read methods for images, networks, and volumes.
func RegisterResources(c *channel.Client, api *dockerapi.Client) {
	c.Calls[protocol.MethodDockerImages] = func(ctx context.Context, _ json.RawMessage) (any, error) {
		result, err := api.Images(ctx)
		return result, mapResourceError(err)
	}
	c.Calls[protocol.MethodDockerNetworks] = func(ctx context.Context, _ json.RawMessage) (any, error) {
		result, err := api.Networks(ctx)
		return result, mapResourceError(err)
	}
	c.Calls[protocol.MethodDockerVolumes] = func(ctx context.Context, _ json.RawMessage) (any, error) {
		result, err := api.Volumes(ctx)
		return result, mapResourceError(err)
	}
	c.Calls[protocol.MethodDockerResourceInspect] = func(ctx context.Context, raw json.RawMessage) (any, error) {
		var params protocol.DockerResourceParams
		if err := decode(raw, &params); err != nil {
			return nil, err
		}
		result, err := api.InspectResource(ctx, params.Kind, params.ID)
		return result, mapResourceError(err)
	}
}

// RegisterResourceManagement adds pulling, removing, and pruning. Call it only for a
// host at the manage tier; the channel also refuses these methods below that tier.
func RegisterResourceManagement(c *channel.Client, api *dockerapi.Client) {
	c.Calls[protocol.MethodDockerResourceRemove] = func(ctx context.Context, raw json.RawMessage) (any, error) {
		var params protocol.DockerResourceParams
		if err := decode(raw, &params); err != nil {
			return nil, err
		}
		result, err := api.RemoveResource(ctx, params)
		return result, mapResourceError(err)
	}
	c.Calls[protocol.MethodDockerPrune] = func(ctx context.Context, raw json.RawMessage) (any, error) {
		var params protocol.DockerPruneParams
		if err := decode(raw, &params); err != nil {
			return nil, err
		}
		result, err := api.Prune(ctx, params)
		return result, mapResourceError(err)
	}
	c.Streams[protocol.MethodDockerPull] = func(ctx context.Context, raw json.RawMessage, send func([]byte) error) error {
		var params protocol.DockerPullParams
		if err := decode(raw, &params); err != nil {
			return err
		}
		err := api.PullImage(ctx, params.Ref, func(event protocol.DockerPullEvent) {
			if line, marshalErr := json.Marshal(event); marshalErr == nil {
				_ = send(append(line, '\n'))
			}
		})
		if err != nil && !errors.Is(err, dockerapi.ErrBadRequest) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			var network net.Error
			if !errors.As(err, &network) {
				return &protocol.ChannelError{Code: protocol.ErrCodePullFailed, Message: clip(err.Error(), 300)}
			}
		}
		return mapResourceError(err)
	}
}

// mapResourceError is mapError for images, networks, and volumes: Docker's refusals
// (403 and 409, such as "in use" or "pre-defined network") keep Docker's own words,
// and a missing resource is described as a resource, not a container.
func mapResourceError(err error) error {
	var apiErr *dockerapi.APIError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &apiErr) && (apiErr.Status == http.StatusForbidden || apiErr.Status == http.StatusConflict):
		message := apiErr.Message
		if message == "" {
			message = "Docker refused the request"
		}
		return &protocol.ChannelError{Code: protocol.ErrCodeConflict, Message: clip(message, 300)}
	case errors.Is(err, dockerapi.ErrNotFound):
		return &protocol.ChannelError{Code: protocol.ErrCodeNotFound, Message: "that resource no longer exists"}
	case errors.Is(err, dockerapi.ErrBadRequest):
		return &protocol.ChannelError{Code: protocol.ErrCodeBadRequest, Message: "the resource reference is invalid"}
	}
	return mapError(err)
}
