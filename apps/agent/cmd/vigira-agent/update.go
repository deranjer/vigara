package main

import (
	"context"
	"encoding/json"

	"github.com/deranjer/vigara/apps/agent/internal/channel"
	"github.com/deranjer/vigara/apps/agent/internal/selfupdate"
)

// Every live operation shares the same cross-process lock as a polling cycle.
// The updater can stop the service only after backups, deployments, and streams
// have finished, and no new operation can start while it owns the lock.
func guardUpdates(live *channel.Client) {
	for name, call := range live.Calls {
		live.Calls[name] = func(ctx context.Context, p json.RawMessage) (any, error) {
			release, err := selfupdate.Operation(ctx)
			if err != nil {
				return nil, err
			}
			defer release()
			return call(ctx, p)
		}
	}
	for name, stream := range live.Streams {
		live.Streams[name] = func(ctx context.Context, p json.RawMessage, send func([]byte) error) error {
			release, err := selfupdate.Operation(ctx)
			if err != nil {
				return err
			}
			defer release()
			return stream(ctx, p, send)
		}
	}
	for name, interactive := range live.Interactive {
		live.Interactive[name] = func(ctx context.Context, p json.RawMessage, stream *channel.StreamIO) error {
			release, err := selfupdate.Operation(ctx)
			if err != nil {
				return err
			}
			defer release()
			return interactive(ctx, p, stream)
		}
	}
}
