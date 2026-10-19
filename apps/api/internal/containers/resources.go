package containers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/audit"
	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/internal/protocol"
)

const (
	resourceTimeout = 30 * time.Second
	pruneTimeout    = 6 * time.Minute
	pullTimeout     = 30 * time.Minute
)

// registerResources mounts the image, network, and volume routes under /api/docker/.
func (a *api) registerResources(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/docker/{agent}/{kind}", a.listResources)
	mux.HandleFunc("GET /api/docker/{agent}/{kind}/inspect", a.inspectResource)
	mux.HandleFunc("POST /api/docker/{agent}/{kind}/remove", a.removeResource)
	mux.HandleFunc("POST /api/docker/{agent}/{kind}/prune", a.pruneResources)
	mux.HandleFunc("POST /api/docker/{agent}/images/pull", a.pullImage)
}

// resourceKinds maps the plural path segment to the resource kind and its list method.
var resourceKinds = map[string]struct{ kind, method string }{
	"images":   {protocol.KindImage, protocol.MethodDockerImages},
	"networks": {protocol.KindNetwork, protocol.MethodDockerNetworks},
	"volumes":  {protocol.KindVolume, protocol.MethodDockerVolumes},
}

// resourceTarget validates the host and resource kind in the path.
func resourceTarget(w http.ResponseWriter, r *http.Request) (agentID, kind, method string, ok bool) {
	agentID = r.PathValue("agent")
	known, found := resourceKinds[r.PathValue("kind")]
	if !uuidPattern.MatchString(agentID) || !found {
		http.Error(w, "not found", http.StatusNotFound)
		return "", "", "", false
	}
	return agentID, known.kind, known.method, true
}

func (a *api) listResources(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.Authz.Authorize(w, r, auth.PermContainersRead); !ok {
		return
	}
	agentID, _, method, ok := resourceTarget(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), resourceTimeout)
	defer cancel()
	var result json.RawMessage
	if err := a.Hub.Call(ctx, agentID, method, nil, &result); err != nil {
		_, message := describe(err)
		http.Error(w, message, statusFor(err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(result)
}

func (a *api) inspectResource(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.Authz.Authorize(w, r, auth.PermContainersRead); !ok {
		return
	}
	agentID, kind, _, ok := resourceTarget(w, r)
	if !ok {
		return
	}
	ref := r.URL.Query().Get("ref")
	if !protocol.ValidResourceRef(kind, ref) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), resourceTimeout)
	defer cancel()
	var result json.RawMessage
	if err := a.Hub.Call(ctx, agentID, protocol.MethodDockerResourceInspect, protocol.DockerResourceParams{Kind: kind, ID: ref}, &result); err != nil {
		_, message := describe(err)
		http.Error(w, message, statusFor(err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(result)
}

func resourceEvent(user auth.User, action, kind, ref, agentID string) audit.Event {
	return audit.Event{ActorID: user.ID, ActorName: user.Username, Action: action, TargetKind: kind, TargetID: ref, AgentID: agentID}
}

func (a *api) removeResource(w http.ResponseWriter, r *http.Request) {
	agentID, kind, _, ok := resourceTarget(w, r)
	if !ok {
		return
	}
	var request struct {
		Ref   string `json:"ref"`
		Force bool   `json:"force"`
	}
	if !decodeBody(w, r, &request) {
		return
	}
	action := kind + ".remove"
	user, ok := a.managerFor(w, r, action, kind, agentID, request.Ref)
	if !ok {
		return
	}
	if !protocol.ValidResourceRef(kind, request.Ref) {
		http.Error(w, "invalid resource reference", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), actionTimeout)
	defer cancel()
	var result protocol.DockerRemoveResult
	err := a.Hub.Call(ctx, agentID, protocol.MethodDockerResourceRemove, protocol.DockerResourceParams{Kind: kind, ID: request.Ref, Force: request.Force}, &result)
	event := resourceEvent(user, action, kind, request.Ref, agentID)
	event.Outcome, event.Detail = audit.OutcomeSuccess, map[string]any{}
	if request.Force {
		event.Detail["force"] = true
	}
	if err != nil {
		event.Outcome, event.Detail["code"] = audit.OutcomeFailure, errorCode(err)
		a.record(context.WithoutCancel(r.Context()), event)
		_, message := describe(err)
		http.Error(w, message, statusFor(err))
		return
	}
	if len(result.Deleted) > 0 {
		event.Detail["deleted"] = len(result.Deleted)
	}
	a.record(context.WithoutCancel(r.Context()), event)
	respond(w, http.StatusOK, result)
}

func (a *api) pruneResources(w http.ResponseWriter, r *http.Request) {
	agentID, kind, _, ok := resourceTarget(w, r)
	if !ok {
		return
	}
	var request struct {
		All    bool `json:"all"`
		DryRun bool `json:"dryRun"`
	}
	if r.ContentLength != 0 && !decodeBody(w, r, &request) {
		return
	}
	action := kind + ".prune"
	user, ok := a.managerFor(w, r, action, kind, agentID, "")
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), pruneTimeout)
	defer cancel()
	var result protocol.DockerPruneResult
	err := a.Hub.Call(ctx, agentID, protocol.MethodDockerPrune, protocol.DockerPruneParams{Kind: kind, All: request.All, DryRun: request.DryRun}, &result)
	// A dry run only reads, so only a real prune is audited.
	if !request.DryRun {
		event := resourceEvent(user, action, kind, "", agentID)
		event.Outcome, event.Detail = audit.OutcomeSuccess, map[string]any{"all": request.All}
		if err != nil {
			event.Outcome, event.Detail["code"] = audit.OutcomeFailure, errorCode(err)
		} else {
			event.Detail["removed"], event.Detail["spaceReclaimed"] = len(result.Removed), result.SpaceReclaimed
		}
		a.record(context.WithoutCancel(r.Context()), event)
	}
	if err != nil {
		_, message := describe(err)
		http.Error(w, message, statusFor(err))
		return
	}
	respond(w, http.StatusOK, result)
}

// pullImage pulls an image on the host and streams progress as newline-delimited JSON.
func (a *api) pullImage(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent")
	if !uuidPattern.MatchString(agentID) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	var request struct {
		Ref string `json:"ref"`
	}
	if !decodeBody(w, r, &request) {
		return
	}
	user, ok := a.managerFor(w, r, "image.pull", protocol.KindImage, agentID, request.Ref)
	if !ok {
		return
	}
	request.Ref = strings.TrimSpace(request.Ref)
	if !protocol.ValidResourceRef(protocol.KindImage, request.Ref) || strings.HasPrefix(request.Ref, "sha256:") {
		http.Error(w, "enter an image name such as nginx:alpine or ghcr.io/owner/app:tag", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), pullTimeout)
	defer cancel()
	event := resourceEvent(user, "image.pull", protocol.KindImage, request.Ref, agentID)
	event.Outcome, event.Detail = audit.OutcomeFailure, map[string]any{}
	stream, err := a.Hub.OpenStream(ctx, agentID, protocol.MethodDockerPull, protocol.DockerPullParams{Ref: request.Ref})
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

	finished, clientLeft := false, false
relay:
	for {
		select {
		case chunk, open := <-stream.C:
			if !open {
				break relay
			}
			for _, line := range bytes.Split(chunk, []byte("\n")) {
				var step protocol.DockerPullEvent
				if len(line) > 0 && json.Unmarshal(line, &step) == nil && step.Step == protocol.StepDone {
					finished = true
				}
			}
			if _, err := w.Write(chunk); err != nil {
				clientLeft = true
				break relay
			}
			flush()
		case <-ctx.Done():
			clientLeft = true
			break relay
		}
	}
	switch streamErr := stream.Err(); {
	case clientLeft:
		event.Detail["code"] = "client_left"
	case streamErr != nil:
		_, message := describe(streamErr)
		event.Detail["code"], event.Detail["message"] = errorCode(streamErr), message
		line, _ := json.Marshal(map[string]string{"step": "error", "code": errorCode(streamErr), "message": message})
		_, _ = w.Write(append(line, '\n'))
	case finished:
		event.Outcome = audit.OutcomeSuccess
	default:
		event.Detail["code"] = "incomplete"
	}
	flush()
	a.record(context.WithoutCancel(r.Context()), event)
}
