package containers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/audit"
	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/internal/protocol"
)

const (
	actionTimeout   = 3 * time.Minute
	recreateTimeout = 20 * time.Minute
)

// manager authorizes a state-changing request. Anyone signed in passes the login
// check first, so a user without the permission is refused and the attempt is
// recorded instead of vanishing behind a bare 403.
func (a *api) manager(w http.ResponseWriter, r *http.Request, action, agentID, containerID string) (auth.User, bool) {
	user, ok := a.Authz.Authorize(w, r, auth.PermContainersRead)
	if !ok {
		return auth.User{}, false
	}
	if !user.Can(auth.PermContainersManage) {
		a.record(r.Context(), audit.Event{ActorID: user.ID, ActorName: user.Username, Action: action, TargetKind: "container", TargetID: containerID, AgentID: agentID, Outcome: audit.OutcomeDenied})
		http.Error(w, "your role does not allow changing containers", http.StatusForbidden)
		return auth.User{}, false
	}
	return user, true
}

func decodeBody(w http.ResponseWriter, r *http.Request, into any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil || decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return false
	}
	return true
}

func validAction(action string) bool {
	for _, known := range protocol.DockerActions {
		if action == known {
			return true
		}
	}
	return false
}

// action runs one lifecycle action. The attempt is audited whether it succeeds,
// fails, or is refused.
func (a *api) action(w http.ResponseWriter, r *http.Request) {
	agentID, containerID, ok := target(w, r)
	if !ok {
		return
	}
	// The container always comes from the path; a body that names one is refused.
	var request struct {
		Action         string `json:"action"`
		Force          bool   `json:"force"`
		RemoveVolumes  bool   `json:"removeVolumes"`
		TimeoutSeconds int    `json:"timeoutSeconds"`
		Signal         string `json:"signal"`
	}
	if !decodeBody(w, r, &request) {
		return
	}
	params := protocol.DockerActionParams{Action: request.Action, Force: request.Force, RemoveVolumes: request.RemoveVolumes, TimeoutSeconds: request.TimeoutSeconds, Signal: request.Signal}
	auditAction := "container." + params.Action
	user, ok := a.manager(w, r, auditAction, agentID, containerID)
	if !ok {
		return
	}
	if !validAction(params.Action) {
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	params.ID = containerID
	ctx, cancel := context.WithTimeout(r.Context(), actionTimeout)
	defer cancel()
	var result protocol.DockerActionResult
	err := a.Hub.Call(ctx, agentID, protocol.MethodDockerAction, params, &result)

	event := audit.Event{ActorID: user.ID, ActorName: user.Username, Action: auditAction, TargetKind: "container", TargetID: containerID, AgentID: agentID,
		Outcome: audit.OutcomeSuccess, Detail: map[string]any{}}
	if params.Force {
		event.Detail["force"] = true
	}
	if params.RemoveVolumes {
		event.Detail["removeVolumes"] = true
	}
	if params.Signal != "" {
		event.Detail["signal"] = params.Signal
	}
	if err != nil {
		event.Outcome = audit.OutcomeFailure
		event.Detail["code"] = errorCode(err)
		a.record(context.WithoutCancel(r.Context()), event)
		_, message := describe(err)
		http.Error(w, message, statusFor(err))
		return
	}
	event.Detail["name"], event.Detail["changed"] = result.Name, result.Changed
	a.record(context.WithoutCancel(r.Context()), event)
	respond(w, http.StatusOK, result)
}

// recreate replaces a container and streams progress as newline-delimited JSON
// over the HTTP response. The browser going away cancels the work only until the
// agent's swap begins; after that the agent always finishes or rolls back.
func (a *api) recreate(w http.ResponseWriter, r *http.Request) {
	agentID, containerID, ok := target(w, r)
	if !ok {
		return
	}
	auditAction := "container." + protocol.ActionRecreate
	user, ok := a.manager(w, r, auditAction, agentID, containerID)
	if !ok {
		return
	}
	var request struct {
		Pull bool `json:"pull"`
	}
	if r.ContentLength != 0 && !decodeBody(w, r, &request) {
		return
	}
	params := protocol.DockerRecreateParams{ID: containerID, Pull: request.Pull}
	ctx, cancel := context.WithTimeout(r.Context(), recreateTimeout)
	defer cancel()

	event := audit.Event{ActorID: user.ID, ActorName: user.Username, Action: auditAction, TargetKind: "container", TargetID: containerID, AgentID: agentID,
		Outcome: audit.OutcomeFailure, Detail: map[string]any{"pull": params.Pull}}
	stream, err := a.Hub.OpenStream(ctx, agentID, protocol.MethodDockerRecreate, params)
	if err != nil {
		event.Detail["code"] = errorCode(err)
		a.record(context.WithoutCancel(r.Context()), event)
		_, message := describe(err)
		http.Error(w, message, statusFor(err))
		return
	}
	defer stream.Close()

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	flush := func() {
		if flusher != nil {
			flusher.Flush()
		}
	}
	flush()

	var done *protocol.DockerRecreateResult
	clientLeft := false
relay:
	for {
		select {
		case chunk, open := <-stream.C:
			if !open {
				break relay
			}
			for _, line := range bytes.Split(chunk, []byte("\n")) {
				var step protocol.DockerRecreateEvent
				if len(line) > 0 && json.Unmarshal(line, &step) == nil && step.Step == protocol.StepDone {
					done = step.Result
				}
			}
			if _, err := w.Write(chunk); err != nil {
				clientLeft = true
				break relay
			}
			flush()
		case <-ctx.Done():
			// The browser left (or the time limit passed). The deferred Close cancels the
			// agent's work as far as it can still be cancelled; after its swap begins the
			// agent finishes regardless, so the outcome is recorded as unknown.
			clientLeft = true
			break relay
		}
	}
	if clientLeft {
		event.Detail["code"] = "client_left"
		event.Detail["note"] = "The browser disconnected; the host may still have completed the change."
		a.record(context.WithoutCancel(r.Context()), event)
		return
	}
	switch streamErr := stream.Err(); {
	case streamErr != nil:
		_, message := describe(streamErr)
		event.Detail["code"], event.Detail["message"] = errorCode(streamErr), message
		line, _ := json.Marshal(map[string]string{"step": "error", "code": errorCode(streamErr), "message": message})
		_, _ = w.Write(append(line, '\n'))
	case done != nil:
		event.Outcome = audit.OutcomeSuccess
		event.Detail["name"], event.Detail["imageChanged"] = done.Name, done.ImageChanged
	default:
		event.Detail["code"] = "incomplete"
	}
	flush()
	a.record(context.WithoutCancel(r.Context()), event)
}
