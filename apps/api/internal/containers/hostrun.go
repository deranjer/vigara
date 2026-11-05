package containers

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/audit"
	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/internal/protocol"
)

const hostRunTimeout = (protocol.HostRunTimeoutSeconds + 10) * time.Second

// hostRun runs one allowlisted read-only command on a device and returns its bounded
// output. It needs the same permission as the device shell, and every request is
// audited with the command and path (never the output).
func (a *api) hostRun(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent")
	if !uuidPattern.MatchString(agentID) {
		http.NotFound(w, r)
		return
	}
	user, ok := a.Authz.Authorize(w, r, auth.PermDevicesRead)
	if !ok {
		return
	}
	event := audit.Event{ActorID: user.ID, ActorName: user.Username, Action: "device.run", TargetKind: "agent", TargetID: agentID, AgentID: agentID}
	if !user.Can(auth.PermDevicesExec) {
		event.Outcome = audit.OutcomeDenied
		a.record(r.Context(), event)
		http.Error(w, "your role does not allow running device commands", http.StatusForbidden)
		return
	}
	var params protocol.HostRunParams
	if !decodeBody(w, r, &params) {
		return
	}
	if err := params.Normalize(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	event.Detail = map[string]any{"command": params.Command}
	if params.Path != "" {
		event.Detail["path"] = params.Path
	}
	ctx, cancel := context.WithTimeout(r.Context(), hostRunTimeout)
	defer cancel()
	var result protocol.HostRunResult
	if err := a.Hub.Call(ctx, agentID, protocol.MethodHostRun, params, &result); err != nil {
		event.Outcome = audit.OutcomeFailure
		event.Detail["code"] = errorCode(err)
		a.record(context.WithoutCancel(r.Context()), event)
		_, message := describe(err)
		var channelErr *protocol.ChannelError
		if errors.As(err, &channelErr) && (channelErr.Code == protocol.ErrCodeBadRequest || channelErr.Code == protocol.ErrCodeExecFailed || channelErr.Code == protocol.ErrCodeTierInsufficient) {
			message = channelErr.Message
		}
		http.Error(w, message, statusFor(err))
		return
	}
	event.Outcome = audit.OutcomeSuccess
	event.Detail["exitCode"], event.Detail["outputBytes"] = result.ExitCode, len(result.Stdout)+len(result.Stderr)
	a.record(context.WithoutCancel(r.Context()), event)
	respond(w, http.StatusOK, result)
}
