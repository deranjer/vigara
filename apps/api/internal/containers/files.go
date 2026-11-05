package containers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/audit"
	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/internal/protocol"
)

func (a *api) registerFiles(mux *http.ServeMux) {
	for operation, method := range map[string]string{"list": protocol.MethodDockerFilesList, "read": protocol.MethodDockerFilesRead, "write": protocol.MethodDockerFilesWrite} {
		mux.HandleFunc("POST /api/docker/{agent}/files/"+operation, a.files(operation, method))
	}
}

func (a *api) files(operation, method string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		// Mounts often contain passwords or private keys. Use the existing
		// administrator-only reveal permission for directory listings as well.
		user, ok := a.Authz.Authorize(w, r, auth.PermSecretsReveal)
		if !ok {
			return
		}
		agentID := r.PathValue("agent")
		if !uuidPattern.MatchString(agentID) {
			http.Error(w, "host not found", http.StatusNotFound)
			return
		}
		var p protocol.DockerFileParams
		r.Body = http.MaxBytesReader(w, r.Body, 6*protocol.MaxDockerFileBytes+8192)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&p); err != nil || decoder.Decode(new(any)) != io.EOF ||
			len(p.Content) > protocol.MaxDockerFileBytes || len(p.Path) > 2048 || len(p.Mount) > 4096 || len(p.Version) > 128 ||
			(p.Kind != "mount" && p.Kind != "volume") ||
			(p.Kind == "mount" && (!refPattern.MatchString(p.ID) || p.Mount == "")) ||
			(p.Kind == "volume" && (!protocol.ValidResourceRef(protocol.KindVolume, p.ID) || p.Mount != "")) ||
			(operation != "write" && (p.Content != "" || p.Version != "")) {
			http.Error(w, "invalid file request", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		var result json.RawMessage
		err := a.Hub.Call(ctx, agentID, method, p, &result)
		event := audit.Event{ActorID: user.ID, ActorName: user.Username, Action: "docker.files." + operation, TargetKind: p.Kind, TargetID: p.ID, AgentID: agentID, Outcome: audit.OutcomeSuccess}
		// Contents, filenames, versions and backup paths stay out of audit logs.
		if err != nil {
			event.Outcome = audit.OutcomeFailure
			event.Detail = map[string]any{"code": errorCode(err)}
		}
		a.record(context.WithoutCancel(r.Context()), event)
		if err != nil {
			var channelErr *protocol.ChannelError
			if errors.As(err, &channelErr) {
				switch channelErr.Code {
				case protocol.ErrCodeFileAccess:
					http.Error(w, channelErr.Message, http.StatusBadRequest)
					return
				case protocol.ErrCodeUnknownMethod:
					http.Error(w, "Mount file access is disabled or the agent needs updating. On a native Linux Docker host, open Device info > Reinstall agent, configure Docker with container terminals or stack deployment, and select Mounted file access: Browse only or Browse and edit. Run the downloaded installer on that host.", http.StatusConflict)
					return
				}
			}
			_, message := describe(err)
			http.Error(w, message, statusFor(err))
			return
		}
		respond(w, http.StatusOK, result)
	}
}
