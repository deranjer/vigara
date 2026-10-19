// Package dockerchannel exposes the read-only Docker client as live channel
// methods. It is registered only when local Docker collection is enabled, so a
// host that has not opted in answers every Docker method as unknown.
package dockerchannel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
