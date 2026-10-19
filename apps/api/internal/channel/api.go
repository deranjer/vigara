package channel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/internal/protocol"
)

// Authorizer is the part of the auth service the channel API needs.
type Authorizer interface {
	Authorize(w http.ResponseWriter, r *http.Request, permission auth.Permission) (auth.User, bool)
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// RegisterAPI mounts the browser-facing routes under /api/channel/. They only
// report connection state and run the harmless ping call; feature routes added
// later must check their own, narrower permissions.
func (h *Hub) RegisterAPI(mux *http.ServeMux, authz Authorizer) {
	respond := func(w http.ResponseWriter, status int, body any) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}
	mux.HandleFunc("GET /api/channel/agents", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := authz.Authorize(w, r, auth.PermContainersRead); !ok {
			return
		}
		respond(w, http.StatusOK, h.Agents())
	})
	mux.HandleFunc("POST /api/channel/agents/{id}/ping", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := authz.Authorize(w, r, auth.PermContainersRead); !ok {
			return
		}
		id := r.PathValue("id")
		if !uuidPattern.MatchString(id) {
			http.Error(w, "agent not found", http.StatusNotFound)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		started := time.Now()
		var result protocol.ChannelPingResult
		if err := h.Call(ctx, id, protocol.MethodPing, nil, &result); err != nil {
			status := http.StatusBadGateway
			switch {
			case errors.Is(err, ErrAgentOffline), errors.Is(err, ErrAgentDisconnected):
				status = http.StatusConflict
			case errors.Is(err, context.DeadlineExceeded):
				status = http.StatusGatewayTimeout
			}
			http.Error(w, "the agent could not be reached over the live channel", status)
			return
		}
		respond(w, http.StatusOK, map[string]any{"roundTripMillis": time.Since(started).Milliseconds(), "agentVersion": result.AgentVersion})
	})
}
