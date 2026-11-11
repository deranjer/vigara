// Package hostshell serves opted-in device terminals over the agent channel.
package hostshell

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/deranjer/vigara/apps/agent/internal/channel"
	"github.com/deranjer/vigara/internal/protocol"
)

type terminal interface {
	io.ReadWriteCloser
	Resize(uint16, uint16) error
	Wait(context.Context) (int, error)
}

func Register(c *channel.Client) {
	registerRun(c)
	c.Interactive[protocol.MethodHostExec] = func(ctx context.Context, raw json.RawMessage, stream *channel.StreamIO) error {
		if err := protocol.CheckHostExec(protocol.MethodHostExec, c.Capabilities()); err != nil {
			return err
		}
		var p protocol.HostExecParams
		if json.Unmarshal(raw, &p) != nil {
			return &protocol.ChannelError{Code: protocol.ErrCodeBadRequest, Message: "Invalid terminal options."}
		}
		if err := p.Normalize(); err != nil {
			return &protocol.ChannelError{Code: protocol.ErrCodeBadRequest, Message: err.Error()}
		}
		term, err := start(p)
		if err != nil {
			return &protocol.ChannelError{Code: protocol.ErrCodeExecFailed, Message: "Could not start the selected device shell: " + err.Error()}
		}
		defer term.Close()
		output := make(chan error, 1)
		go func() {
			buffer := make([]byte, protocol.MaxStreamChunkBytes)
			for {
				n, err := term.Read(buffer)
				if n > 0 {
					if sendErr := stream.Send(buffer[:n]); sendErr != nil {
						output <- sendErr
						return
					}
				}
				if err != nil {
					output <- nil
					return
				}
			}
		}()
		exited := make(chan error, 1)
		go func() {
			code, err := term.Wait(ctx)
			if err == nil && code != 0 {
				err = &protocol.ChannelError{Code: protocol.ErrCodeExecExit, Message: fmt.Sprintf("The shell exited with status %d.", code)}
			}
			exited <- err
		}()
		input, control := stream.Input, stream.Control
		var exitErr error
		var drain <-chan time.Time
		var drainTimer *time.Timer
		defer func() {
			if drainTimer != nil {
				drainTimer.Stop()
			}
		}()
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case err := <-output:
				if err != nil {
					return err
				}
				if exited == nil {
					return exitErr
				}
				select {
				case err := <-exited:
					return err
				case <-ctx.Done():
					return ctx.Err()
				}
			case err := <-exited:
				exitErr, exited = err, nil
				// Drain final output. ConPTY keeps its pipe open until Close, so bound the wait.
				drainTimer = time.NewTimer(time.Second)
				drain = drainTimer.C
			case <-drain:
				return exitErr
			case data, open := <-input:
				if !open {
					return nil
				}
				if _, err := term.Write(data); err != nil {
					return err
				}
			case raw, open := <-control:
				if !open {
					control = nil
					continue
				}
				var c protocol.ExecControl
				if json.Unmarshal(raw, &c) == nil && c.Type == protocol.ControlResize {
					_ = term.Resize(protocol.ClampExecSize(c.Cols, 80), protocol.ClampExecSize(c.Rows, 24))
				}
			}
		}
	}
}
