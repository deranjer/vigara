// Package containers serves the browser-facing container views: a fleet-wide
// list, per-container inspect, and live log and statistics streams. Everything
// is read-only and travels over each agent's live channel.
package containers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/deranjer/vigara/apps/api/internal/audit"
	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/apps/api/internal/channel"
	"github.com/deranjer/vigara/internal/protocol"
)

const (
	hostTimeout     = 8 * time.Second
	inspectTimeout  = 10 * time.Second
	maxStreamLength = time.Hour
	writeTimeout    = 10 * time.Second
)

var (
	uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	refPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
)

// AuditLog records who did what. *audit.Recorder satisfies it.
type AuditLog interface {
	Record(ctx context.Context, event audit.Event) error
}

// Config wires the API to its collaborators.
type Config struct {
	Hub   *channel.Hub
	Authz channel.Authorizer
	Audit AuditLog
	// Names maps every enrolled agent's ID to its device name, so hosts that are
	// offline can still be listed.
	Names func(ctx context.Context) (map[string]string, error)
}

// Host states reported by the fleet list.
const (
	StateOK          = "ok"
	StateOffline     = "offline"
	StateUnavailable = "unavailable" // connected, but Docker access is not enabled on the host
	StateError       = "error"
)

// Host is one machine's slice of the fleet list.
type Host struct {
	AgentID    string                         `json:"agentId"`
	Name       string                         `json:"name"`
	State      string                         `json:"state"`
	Message    string                         `json:"message,omitempty"`
	DockerTier string                         `json:"dockerTier,omitempty"`
	Containers []protocol.DockerContainerInfo `json:"containers"`
	Truncated  bool                           `json:"truncated,omitempty"`
}

type api struct{ Config }

// RegisterAPI mounts the routes under /api/containers/.
func RegisterAPI(mux *http.ServeMux, cfg Config) {
	a := &api{cfg}
	mux.HandleFunc("GET /api/containers", a.fleet)
	mux.HandleFunc("GET /api/containers/{agent}/{id}/inspect", a.inspect)
	mux.HandleFunc("GET /api/containers/{agent}/{id}/logs", a.stream(protocol.MethodDockerLogs))
	mux.HandleFunc("GET /api/containers/{agent}/{id}/stats", a.stream(protocol.MethodDockerStats))
	mux.HandleFunc("POST /api/containers/{agent}/{id}/actions", a.action)
	mux.HandleFunc("POST /api/containers/{agent}/{id}/recreate", a.recreate)
}

func respond(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// describe turns a channel failure into a host state and a message that is safe
// to show to a user.
func describe(err error) (state, message string) {
	var channelErr *protocol.ChannelError
	switch {
	case errors.Is(err, channel.ErrAgentOffline), errors.Is(err, channel.ErrAgentDisconnected):
		return StateOffline, "This host is not connected."
	case errors.As(err, &channelErr):
		switch channelErr.Code {
		case protocol.ErrCodeUnknownMethod:
			return StateUnavailable, "Docker access is not enabled on this host. Set VIGIRA_DOCKER_ENABLED=true and VIGIRA_DOCKER_HOST on its agent."
		case protocol.ErrCodeDockerDown:
			return StateError, "Docker could not be reached on this host."
		case protocol.ErrCodeNotFound:
			return StateError, "That container no longer exists."
		case protocol.ErrCodeTierInsufficient:
			return StateError, "Container management is not enabled for this host. Enable it on the host (VIGIRA_DOCKER_MODE) and allow it in the device's enrollment."
		case protocol.ErrCodeConflict, protocol.ErrCodeRecreateFailed:
			// Docker's own explanation, or which step failed and whether the original was restored.
			return StateError, channelErr.Message
		case protocol.ErrCodeBusy:
			return StateError, "This host is busy; try again in a moment."
		case protocol.ErrCodeTimeout, protocol.ErrCodeCancelled:
			return StateError, "The host did not answer in time."
		}
	case errors.Is(err, context.DeadlineExceeded):
		return StateError, "The host did not answer in time."
	}
	return StateError, "The host could not complete the request."
}

func statusFor(err error) int {
	var channelErr *protocol.ChannelError
	switch {
	case errors.Is(err, channel.ErrAgentOffline), errors.Is(err, channel.ErrAgentDisconnected):
		return http.StatusConflict
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout
	case errors.As(err, &channelErr):
		switch channelErr.Code {
		case protocol.ErrCodeNotFound:
			return http.StatusNotFound
		case protocol.ErrCodeUnknownMethod, protocol.ErrCodeTierInsufficient, protocol.ErrCodeConflict:
			return http.StatusConflict
		case protocol.ErrCodeRecreateFailed:
			return http.StatusBadGateway
		case protocol.ErrCodeBadRequest:
			return http.StatusBadRequest
		case protocol.ErrCodeBusy:
			return http.StatusServiceUnavailable
		case protocol.ErrCodeTimeout:
			return http.StatusGatewayTimeout
		}
	}
	return http.StatusBadGateway
}

func (a *api) fleet(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.Authz.Authorize(w, r, auth.PermContainersRead); !ok {
		return
	}
	names := map[string]string{}
	if a.Names != nil {
		var err error
		if names, err = a.Names(r.Context()); err != nil {
			http.Error(w, "could not read the host list", http.StatusInternalServerError)
			return
		}
	}
	online := map[string]channel.AgentInfo{}
	for _, agent := range a.Hub.Agents() {
		online[agent.AgentID] = agent
		if _, known := names[agent.AgentID]; !known {
			names[agent.AgentID] = agent.AgentID[:8]
		}
	}
	hosts := make([]Host, 0, len(names))
	var mu sync.Mutex
	var group sync.WaitGroup
	for id, name := range names {
		host := Host{AgentID: id, Name: name, State: StateOffline, Message: "This host is not connected.", Containers: []protocol.DockerContainerInfo{}}
		info, connected := online[id]
		if !connected {
			hosts = append(hosts, host)
			continue
		}
		host.DockerTier = info.DockerTier
		group.Add(1)
		go func() {
			defer group.Done()
			ctx, cancel := context.WithTimeout(r.Context(), hostTimeout)
			defer cancel()
			var result protocol.DockerListResult
			err := a.Hub.Call(ctx, id, protocol.MethodDockerList, nil, &result)
			if err != nil {
				host.State, host.Message = describe(err)
			} else {
				host.State, host.Message, host.Truncated = StateOK, "", result.Truncated
				if result.Containers != nil {
					host.Containers = result.Containers
				}
			}
			mu.Lock()
			hosts = append(hosts, host)
			mu.Unlock()
		}()
	}
	group.Wait()
	sort.Slice(hosts, func(i, j int) bool {
		if hosts[i].Name != hosts[j].Name {
			return hosts[i].Name < hosts[j].Name
		}
		return hosts[i].AgentID < hosts[j].AgentID
	})
	respond(w, http.StatusOK, map[string]any{"hosts": hosts})
}

func (a *api) record(ctx context.Context, event audit.Event) {
	if a.Audit != nil {
		_ = a.Audit.Record(ctx, event)
	}
}

// target validates the agent and container references in the path.
func target(w http.ResponseWriter, r *http.Request) (agentID, containerID string, ok bool) {
	agentID, containerID = r.PathValue("agent"), r.PathValue("id")
	if !uuidPattern.MatchString(agentID) || !refPattern.MatchString(containerID) {
		http.Error(w, "container not found", http.StatusNotFound)
		return "", "", false
	}
	return agentID, containerID, true
}

func (a *api) inspect(w http.ResponseWriter, r *http.Request) {
	user, ok := a.Authz.Authorize(w, r, auth.PermContainersRead)
	if !ok {
		return
	}
	agentID, containerID, ok := target(w, r)
	if !ok {
		return
	}
	reveal := r.URL.Query().Get("reveal") == "env"
	event := audit.Event{ActorID: user.ID, ActorName: user.Username, Action: "container.env.reveal", TargetKind: "container", TargetID: containerID, AgentID: agentID}
	if reveal && !user.Can(auth.PermSecretsReveal) {
		event.Outcome = audit.OutcomeDenied
		a.record(r.Context(), event)
		http.Error(w, "your role does not allow revealing environment values", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), inspectTimeout)
	defer cancel()
	var result protocol.DockerInspectResult
	err := a.Hub.Call(ctx, agentID, protocol.MethodDockerInspect, protocol.DockerInspectParams{ID: containerID, RevealEnv: reveal}, &result)
	if reveal {
		// Revealing is recorded whether or not the host answered, with no values.
		event.Outcome = audit.OutcomeSuccess
		if err != nil {
			event.Outcome = audit.OutcomeFailure
		}
		a.record(r.Context(), event)
	}
	if err != nil {
		_, message := describe(err)
		http.Error(w, message, statusFor(err))
		return
	}
	respond(w, http.StatusOK, result)
}

// endMessage is the last message on a stream: it tells the browser why it ended.
type endMessage struct {
	End     bool   `json:"end"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

func (a *api) stream(method string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := a.Authz.Authorize(w, r, auth.PermContainersRead); !ok {
			return
		}
		agentID, containerID, ok := target(w, r)
		if !ok {
			return
		}
		var params any
		query := r.URL.Query()
		switch method {
		case protocol.MethodDockerLogs:
			tail, _ := strconv.Atoi(query.Get("tail"))
			if tail < 0 {
				tail = 0
			}
			params = protocol.DockerLogsParams{ID: containerID, Tail: tail, Follow: query.Get("follow") != "false", Timestamps: query.Get("timestamps") == "true", Since: query.Get("since")}
		default:
			params = protocol.DockerStatsParams{ID: containerID}
		}
		// The WebSocket library rejects cross-origin upgrades, so a page on another
		// site cannot drive a signed-in user's session into this stream.
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		ws.SetReadLimit(1024)
		ctx, cancel := context.WithTimeout(r.Context(), maxStreamLength)
		defer cancel()
		go func() { // The browser sends nothing; reading detects it going away.
			for {
				if _, _, err := ws.Read(ctx); err != nil {
					cancel()
					return
				}
			}
		}()
		finish := func(end endMessage) {
			data, _ := json.Marshal(end)
			writeCtx, done := context.WithTimeout(context.Background(), writeTimeout)
			defer done()
			_ = ws.Write(writeCtx, websocket.MessageText, data)
			_ = ws.Close(websocket.StatusNormalClosure, "")
		}
		stream, err := a.Hub.OpenStream(ctx, agentID, method, params)
		if err != nil {
			_, message := describe(err)
			finish(endMessage{End: true, Code: errorCode(err), Message: message})
			return
		}
		defer stream.Close()
		for {
			select {
			case chunk, open := <-stream.C:
				if !open {
					end := endMessage{End: true}
					if streamErr := stream.Err(); streamErr != nil {
						_, end.Message = describe(streamErr)
						end.Code = errorCode(streamErr)
					}
					finish(end)
					return
				}
				writeCtx, done := context.WithTimeout(ctx, writeTimeout)
				err := ws.Write(writeCtx, websocket.MessageText, chunk)
				done()
				if err != nil {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}
}

func errorCode(err error) string {
	var channelErr *protocol.ChannelError
	if errors.As(err, &channelErr) {
		return channelErr.Code
	}
	return "error"
}
