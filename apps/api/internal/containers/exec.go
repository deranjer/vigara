package containers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/deranjer/vigara/apps/api/internal/audit"
	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/internal/protocol"
)

// maxTerminalsPerUser bounds how many terminals one person can hold open, so a
// runaway page cannot exhaust a host's stream capacity.
const maxTerminalsPerUser = 8

// Reasons a terminal ended, recorded in the audit log and shown to the user.
const (
	reasonShellExit    = "shell_exit"
	reasonShellFailed  = "shell_exit_nonzero"
	reasonClientLeft   = "client_left"
	reasonIdle         = "idle_timeout"
	reasonMaxDuration  = "max_duration"
	reasonStartFailed  = "start_failed"
	reasonTooManyOpen  = "too_many_terminals"
	reasonStreamFailed = "stream_failed"
)

type terminalCounts struct {
	mu     sync.Mutex
	active map[string]int
}

func (t *terminalCounts) acquire(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.active == nil {
		t.active = map[string]int{}
	}
	if t.active[key] >= maxTerminalsPerUser {
		return false
	}
	t.active[key]++
	return true
}

func (t *terminalCounts) release(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.active[key] <= 1 {
		delete(t.active, key)
		return
	}
	t.active[key]--
}

func parseSize(value string) uint16 {
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 || n > 65535 {
		return 0
	}
	return uint16(n)
}

// exec opens an interactive terminal. The browser speaks over a same-origin
// WebSocket: binary messages are keystrokes in and terminal output out, and text
// messages carry control (a resize in, and a final end message out). Opening and
// closing a terminal are both audited, with sizes and counts but never content.
func (a *api) exec(w http.ResponseWriter, r *http.Request) {
	agentID, containerID, ok := target(w, r)
	if !ok {
		return
	}
	user, ok := a.Authz.Authorize(w, r, auth.PermContainersRead)
	if !ok {
		return
	}
	openEvent := audit.Event{ActorID: user.ID, ActorName: user.Username, Action: "container.exec.open", TargetKind: "container", TargetID: containerID, AgentID: agentID}
	if !user.Can(auth.PermContainersExec) {
		openEvent.Outcome = audit.OutcomeDenied
		a.record(r.Context(), openEvent)
		http.Error(w, "your role does not allow opening terminals", http.StatusForbidden)
		return
	}
	query := r.URL.Query()
	params := protocol.DockerExecParams{ID: containerID, Shell: query.Get("shell"), User: query.Get("user"), Cols: parseSize(query.Get("cols")), Rows: parseSize(query.Get("rows"))}
	if err := params.Normalize(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// The WebSocket library rejects cross-origin upgrades, so another site cannot
	// drive a signed-in user's session into a terminal.
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer ws.CloseNow()
	// A paste can be large, so a single message may be up to 1 MiB; it is forwarded to the
	// host in pieces no larger than protocol.MaxExecInput.
	ws.SetReadLimit(1 << 20)

	ctx, cancel := context.WithTimeout(r.Context(), a.ExecMax)
	defer cancel()
	finish := func(end endMessage) {
		data, _ := json.Marshal(end)
		writeCtx, done := context.WithTimeout(context.Background(), writeTimeout)
		defer done()
		_ = ws.Write(writeCtx, websocket.MessageText, data)
		_ = ws.Close(websocket.StatusNormalClosure, "")
	}
	detail := map[string]any{"shell": params.Shell}
	if params.User != "" {
		detail["user"] = params.User
	}
	record := func(event audit.Event, outcome string, extra map[string]any) {
		merged := map[string]any{}
		for key, value := range detail {
			merged[key] = value
		}
		for key, value := range extra {
			merged[key] = value
		}
		event.Outcome, event.Detail = outcome, merged
		a.record(context.WithoutCancel(r.Context()), event)
	}

	key := user.ID
	if key == "" {
		key = user.Username
	}
	if !a.terminals.acquire(key) {
		record(openEvent, audit.OutcomeFailure, map[string]any{"reason": reasonTooManyOpen})
		finish(endMessage{End: true, Code: reasonTooManyOpen, Message: "You already have the maximum number of terminals open."})
		return
	}
	released := false
	defer func() {
		if !released {
			a.terminals.release(key)
		}
	}()

	stream, err := a.Hub.OpenStream(ctx, agentID, protocol.MethodDockerExec, params)
	if err != nil {
		_, message := describe(err)
		var channelErr *protocol.ChannelError
		if errors.As(err, &channelErr) && channelErr.Code == protocol.ErrCodeTierInsufficient {
			message = "Terminals are not enabled for this host. Enable them on the host (VIGIRA_DOCKER_MODE=exec) and allow them in the device's enrollment."
		}
		record(openEvent, audit.OutcomeFailure, map[string]any{"reason": reasonStartFailed, "code": errorCode(err)})
		finish(endMessage{End: true, Code: errorCode(err), Message: message})
		return
	}
	defer stream.Close()
	started := time.Now()
	record(openEvent, audit.OutcomeSuccess, nil)

	var bytesIn, bytesOut atomic.Int64
	var lastInput atomic.Int64
	lastInput.Store(started.UnixNano())
	// The WebSocket library drops the connection when a Read context expires, which would
	// swallow the end message. So reading uses its own context, cancelled only on return,
	// and the time limit is enforced by the relay loop below.
	readCtx, stopReading := context.WithCancel(r.Context())
	defer stopReading()
	left := make(chan struct{})
	go func() { // keystrokes and resizes from the browser
		defer close(left)
		for {
			kind, data, err := ws.Read(readCtx)
			if err != nil {
				return
			}
			switch kind {
			case websocket.MessageBinary:
				lastInput.Store(time.Now().UnixNano())
				bytesIn.Add(int64(len(data)))
				for len(data) > 0 { // a large paste is sent in bounded pieces
					piece := data
					if len(piece) > protocol.MaxExecInput {
						piece = piece[:protocol.MaxExecInput]
					}
					data = data[len(piece):]
					if err := stream.Send(ctx, piece); err != nil {
						return
					}
				}
			case websocket.MessageText:
				var control protocol.ExecControl
				if json.Unmarshal(data, &control) != nil || control.Type != protocol.ControlResize {
					continue
				}
				control.Cols = protocol.ClampExecSize(control.Cols, protocol.DefaultExecCols)
				control.Rows = protocol.ClampExecSize(control.Rows, protocol.DefaultExecRows)
				if err := stream.Control(ctx, control); err != nil {
					return
				}
			}
		}
	}()

	reason, end := reasonShellExit, endMessage{End: true}
	checkEvery := min(a.ExecIdle/4, 15*time.Second)
	if checkEvery < 10*time.Millisecond {
		checkEvery = 10 * time.Millisecond
	}
	ticker := time.NewTicker(checkEvery)
	defer ticker.Stop()
relay:
	for {
		select {
		case chunk, open := <-stream.C:
			if !open {
				if streamErr := stream.Err(); streamErr != nil {
					var channelErr *protocol.ChannelError
					if errors.As(streamErr, &channelErr) && channelErr.Code == protocol.ErrCodeExecExit {
						reason, end = reasonShellFailed, endMessage{End: true, Code: channelErr.Code, Message: channelErr.Message}
					} else {
						_, message := describe(streamErr)
						if errors.As(streamErr, &channelErr) && (channelErr.Code == protocol.ErrCodeExecFailed || channelErr.Code == protocol.ErrCodeConflict) {
							message = channelErr.Message
						}
						reason, end = reasonStreamFailed, endMessage{End: true, Code: errorCode(streamErr), Message: message}
					}
				}
				break relay
			}
			writeCtx, done := context.WithTimeout(ctx, writeTimeout)
			err := ws.Write(writeCtx, websocket.MessageBinary, chunk)
			done()
			if err != nil {
				reason, end = reasonClientLeft, endMessage{}
				break relay
			}
			bytesOut.Add(int64(len(chunk)))
		case <-ticker.C:
			if time.Since(time.Unix(0, lastInput.Load())) >= a.ExecIdle {
				reason, end = reasonIdle, endMessage{End: true, Code: reasonIdle, Message: "The terminal was closed after " + a.ExecIdle.String() + " without input."}
				break relay
			}
		case <-left:
			// The reader also stops when the time limit expires, so check that first.
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				reason, end = reasonMaxDuration, endMessage{End: true, Code: reasonMaxDuration, Message: "The terminal was closed because it reached its maximum duration of " + a.ExecMax.String() + "."}
			} else {
				reason, end = reasonClientLeft, endMessage{}
			}
			break relay
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				reason, end = reasonMaxDuration, endMessage{End: true, Code: reasonMaxDuration, Message: "The terminal was closed because it reached its maximum duration of " + a.ExecMax.String() + "."}
			} else {
				reason, end = reasonClientLeft, endMessage{}
			}
			break relay
		}
	}

	record(audit.Event{ActorID: user.ID, ActorName: user.Username, Action: "container.exec.close", TargetKind: "container", TargetID: containerID, AgentID: agentID}, audit.OutcomeSuccess,
		map[string]any{"reason": reason, "durationSeconds": int(time.Since(started).Seconds()), "bytesIn": bytesIn.Load(), "bytesOut": bytesOut.Load()})
	// Stop the shell and free the slot before the closing handshake, which can wait on the browser.
	stream.Close()
	a.terminals.release(key)
	released = true
	if end.End {
		finish(end)
	}
}

