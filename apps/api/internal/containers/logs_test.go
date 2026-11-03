package containers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/internal/protocol"
)

func TestLogSnapshotOptionsAndRecords(t *testing.T) {
	f := newFixture(t)
	agent := f.connectAgent(agentDocker)
	done := make(chan *http.Response, 1)
	go func() {
		done <- f.get("/api/containers/"+agentDocker+"/web/logs/snapshot?tail=50&timestamps=true&since=2026-11-03T12:00:00Z", auth.RoleViewer)
	}()
	open := agent.recv()
	var params protocol.DockerLogsParams
	if open.Type != protocol.FrameStreamOpen || open.Method != protocol.MethodDockerLogs || json.Unmarshal(open.Params, &params) != nil ||
		params.ID != "web" || params.Tail != 50 || params.Follow || !params.Timestamps || params.Since != "2026-11-03T12:00:00Z" {
		t.Fatalf("unexpected stream: %+v params %+v", open, params)
	}
	agent.send(protocol.ChannelFrame{Type: protocol.FrameStreamData, ID: open.ID, Seq: 1, Data: []byte("{\"s\":\"o\",\"t\":\"2026-11-03T12:00:01Z\",\"m\":\"hello\"}\n{\"s\":\"e\",\"m\":\"error\",\"x\":true}\n")})
	agent.send(protocol.ChannelFrame{Type: protocol.FrameStreamEnd, ID: open.ID})
	resp := <-done
	var result logSnapshotResult
	decode(t, resp, &result)
	if resp.StatusCode != http.StatusOK || len(result.Lines) != 2 || result.Lines[0].M != "hello" || result.Lines[0].T == "" || result.Lines[1].S != "e" || !result.Truncated {
		t.Fatalf("snapshot = %d %+v", resp.StatusCode, result)
	}
}

func TestLogSnapshotRejectsInvalidRequestsAndOfflineAgent(t *testing.T) {
	f := newFixture(t)
	path := "/api/containers/" + agentOffline + "/web/logs/snapshot"
	for _, tc := range []struct {
		query, role string
		status      int
	}{
		{"", "", http.StatusUnauthorized},
		{"?tail=0", auth.RoleViewer, http.StatusBadRequest},
		{"?tail=-1", auth.RoleViewer, http.StatusBadRequest},
		{"?tail=5001", auth.RoleViewer, http.StatusBadRequest},
		{"?tail=all", auth.RoleViewer, http.StatusBadRequest},
		{"?since=yesterday", auth.RoleViewer, http.StatusBadRequest},
		{"", auth.RoleViewer, http.StatusConflict},
	} {
		resp := f.get(path+tc.query, tc.role)
		if resp.StatusCode != tc.status {
			t.Fatalf("%s role=%q: got %d want %d", tc.query, tc.role, resp.StatusCode, tc.status)
		}
	}
}

func TestLogSnapshotBoundsAndStreamFailures(t *testing.T) {
	for _, tc := range []struct {
		name, line string
		count      int
		streamErr  *protocol.ChannelError
		status     int
		truncated  bool
	}{
		{"empty", "", 0, nil, http.StatusOK, false},
		{"bytes", `{"s":"o","m":"` + strings.Repeat("x", protocol.MaxLogLineBytes) + `"}`, 70, nil, http.StatusOK, true},
		{"lines", `{"s":"o","m":"hello"}`, 201, nil, http.StatusOK, true},
		{"malformed", `broken`, 1, nil, http.StatusBadGateway, false},
		{"agent failure", `{"s":"o","m":"partial"}`, 1, &protocol.ChannelError{Code: protocol.ErrCodeNotFound, Message: "gone"}, http.StatusNotFound, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			agent := f.connectAgent(agentDocker)
			done := make(chan *http.Response, 1)
			go func() { done <- f.get("/api/containers/"+agentDocker+"/web/logs/snapshot", auth.RoleViewer) }()
			open := agent.recv()
			var params protocol.DockerLogsParams
			if json.Unmarshal(open.Params, &params) != nil || params.Tail != protocol.DefaultLogTail || params.Follow {
				t.Fatalf("default params = %+v", params)
			}
			// Keep each simulated agent frame within the channel size limit.
			if tc.count > 0 {
				if tc.name == "bytes" {
					for i := 0; i < tc.count; i++ {
						agent.send(protocol.ChannelFrame{Type: protocol.FrameStreamData, ID: open.ID, Seq: uint64(i + 1), Data: []byte(tc.line + "\n")})
					}
				} else {
					agent.send(protocol.ChannelFrame{Type: protocol.FrameStreamData, ID: open.ID, Seq: 1, Data: []byte(strings.Repeat(tc.line+"\n", tc.count))})
				}
			}
			if !tc.truncated && tc.name != "malformed" {
				agent.send(protocol.ChannelFrame{Type: protocol.FrameStreamEnd, ID: open.ID, Error: tc.streamErr})
			}
			resp := <-done
			if resp.StatusCode != tc.status {
				t.Fatalf("status = %d want %d", resp.StatusCode, tc.status)
			}
			if tc.status == http.StatusOK {
				var result logSnapshotResult
				decode(t, resp, &result)
				if result.Truncated != tc.truncated || result.Lines == nil {
					t.Fatalf("unexpected snapshot: %+v", result)
				}
			}
			if tc.truncated || tc.name == "malformed" {
				if frame := agent.recv(); frame.Type != protocol.FrameCancel || frame.ID != open.ID {
					t.Fatalf("expected stream cancellation, got %+v", frame)
				}
			}
		})
	}
}
