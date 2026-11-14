package containers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/internal/protocol"
)

const maxLogSnapshotBytes = 1 << 20

type logSnapshotResult struct {
	Lines     []protocol.DockerLogLine `json:"lines"`
	Truncated bool                     `json:"truncated"`
}

// logSnapshot collects a finite, bounded log read over the existing agent channel.
// Live following remains available through the browser's WebSocket route.
func (a *api) logSnapshot(w http.ResponseWriter, r *http.Request) {
	user, ok := a.Authz.Authorize(w, r, auth.PermContainersRead)
	if !ok {
		return
	}
	agentID, containerID, ok := target(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	if !a.requireResource(w, r, user, agentID, containerID, "logs") {
		return
	}
	tail := protocol.DefaultLogTail
	if query.Has("tail") {
		var err error
		tail, err = strconv.Atoi(query.Get("tail"))
		if err != nil || tail < 1 || tail > protocol.MaxLogTail {
			http.Error(w, "tail must be between 1 and 5000", http.StatusBadRequest)
			return
		}
	}
	since := query.Get("since")
	if since != "" {
		if _, err := time.Parse(time.RFC3339Nano, since); err != nil {
			http.Error(w, "since must be an RFC3339 timestamp", http.StatusBadRequest)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), inspectTimeout)
	defer cancel()
	stream, err := a.Hub.OpenStream(ctx, agentID, protocol.MethodDockerLogs, protocol.DockerLogsParams{
		ID: containerID, Tail: tail, Follow: false, Timestamps: query.Get("timestamps") == "true", Since: since,
	})
	if err != nil {
		_, message := describe(err)
		http.Error(w, message, statusFor(err))
		return
	}
	defer stream.Close()
	result := logSnapshotResult{Lines: []protocol.DockerLogLine{}}
	encodedBytes := 0
	for {
		select {
		case chunk, open := <-stream.C:
			if !open {
				if err := stream.Err(); err != nil {
					_, message := describe(err)
					http.Error(w, message, statusFor(err))
					return
				}
				respond(w, http.StatusOK, result)
				return
			}
			// Agent batches contain complete newline-delimited JSON log records.
			for _, raw := range bytes.Split(chunk, []byte("\n")) {
				if len(bytes.TrimSpace(raw)) == 0 {
					continue
				}
				var line protocol.DockerLogLine
				if json.Unmarshal(raw, &line) != nil {
					http.Error(w, "invalid container log response", http.StatusBadGateway)
					return
				}
				encoded, _ := json.Marshal(line)
				if len(result.Lines) >= tail || encodedBytes+len(encoded)+1 > maxLogSnapshotBytes {
					result.Truncated = true
					respond(w, http.StatusOK, result)
					return
				}
				encodedBytes += len(encoded) + 1
				result.Lines = append(result.Lines, line)
				result.Truncated = result.Truncated || line.X
			}
		case <-ctx.Done():
			_, message := describe(ctx.Err())
			http.Error(w, message, statusFor(ctx.Err()))
			return
		}
	}
}
