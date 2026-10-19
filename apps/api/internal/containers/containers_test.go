package containers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/deranjer/vigara/apps/api/internal/audit"
	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/apps/api/internal/channel"
	"github.com/deranjer/vigara/internal/protocol"
)

const (
	agentDocker  = "11111111-1111-4111-8111-111111111111"
	agentPlain   = "22222222-2222-4222-8222-222222222222"
	agentOffline = "33333333-3333-4333-8333-333333333333"
)

// fakeAuthz maps the "role" cookie to a user, enforcing the real permission table.
type fakeAuthz struct{}

func (fakeAuthz) Authorize(w http.ResponseWriter, r *http.Request, permission auth.Permission) (auth.User, bool) {
	if !auth.SameOrigin(r) {
		http.Error(w, "request origin rejected", http.StatusForbidden)
		return auth.User{}, false
	}
	cookie, err := r.Cookie("role")
	if err != nil {
		http.Error(w, "login required", http.StatusUnauthorized)
		return auth.User{}, false
	}
	user := auth.User{Username: cookie.Value, Role: cookie.Value}
	if !user.Can(permission) {
		http.Error(w, "your role does not allow this action", http.StatusForbidden)
		return auth.User{}, false
	}
	return user, true
}

type captureAudit struct {
	mu     sync.Mutex
	events []audit.Event
}

func (c *captureAudit) Record(_ context.Context, event audit.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
	return nil
}

type fixture struct {
	audit  *captureAudit
	t      *testing.T
	hub    *channel.Hub
	server *httptest.Server // serves both the agent channel and the browser API
}

type agentEnd struct {
	t  *testing.T
	ws *websocket.Conn
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	capabilities := map[string][]string{agentDocker: {}, agentPlain: {}}
	hub := channel.NewHub(channel.Options{
		AuthenticateAgent: func(r *http.Request) (string, error) {
			id := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if _, ok := capabilities[id]; !ok {
				return "", errors.New("unknown")
			}
			return id, nil
		},
		Capabilities: func(_ context.Context, id string) ([]string, error) { return capabilities[id], nil },
		PingInterval: time.Hour,
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/api/agent/channel", hub.ServeAgent)
	captured := &captureAudit{}
	RegisterAPI(mux, Config{Hub: hub, Authz: fakeAuthz{}, Audit: captured, Names: func(context.Context) (map[string]string, error) {
		return map[string]string{agentDocker: "nas", agentOffline: "attic"}, nil
	}})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return &fixture{t: t, hub: hub, server: server, audit: captured}
}

func (f *fixture) connectAgent(id string) *agentEnd {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(f.server.URL, "http")+"/api/agent/channel", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer " + id}}})
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { ws.CloseNow() })
	a := &agentEnd{t: f.t, ws: ws}
	a.send(protocol.ChannelFrame{Type: protocol.FrameHello, Version: protocol.ChannelVersion, AgentVersion: "t"})
	if welcome := a.recv(); welcome.Type != protocol.FrameWelcome {
		f.t.Fatalf("welcome = %+v", welcome)
	}
	for i := 0; i < 100; i++ {
		if _, ok := f.hub.Agent(id); ok {
			return a
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatal("agent never registered")
	return nil
}

func (a *agentEnd) send(frame protocol.ChannelFrame) {
	a.t.Helper()
	data, _ := json.Marshal(frame)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.ws.Write(ctx, websocket.MessageText, data); err != nil {
		a.t.Fatal(err)
	}
}

func (a *agentEnd) recv() protocol.ChannelFrame {
	a.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := a.ws.Read(ctx)
	if err != nil {
		a.t.Fatal(err)
	}
	var frame protocol.ChannelFrame
	if err := json.Unmarshal(data, &frame); err != nil {
		a.t.Fatal(err)
	}
	return frame
}

// serve answers every call using handler until the connection closes.
func (a *agentEnd) serve(handler func(call protocol.ChannelFrame) protocol.ChannelFrame) {
	go func() {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			_, data, err := a.ws.Read(ctx)
			cancel()
			if err != nil {
				return
			}
			var frame protocol.ChannelFrame
			if json.Unmarshal(data, &frame) != nil || frame.Type != protocol.FrameCall {
				continue
			}
			reply := handler(frame)
			reply.Type, reply.ID = protocol.FrameResult, frame.ID
			out, _ := json.Marshal(reply)
			_ = a.ws.Write(context.Background(), websocket.MessageText, out)
		}
	}()
}

func (f *fixture) get(path, role string) *http.Response {
	f.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, f.server.URL+path, nil)
	if role != "" {
		req.AddCookie(&http.Cookie{Name: "role", Value: role})
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func decode(t *testing.T, resp *http.Response, into any) {
	t.Helper()
	if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
		t.Fatal(err)
	}
}

func TestFleetListsEveryHostWithItsState(t *testing.T) {
	f := newFixture(t)
	docker := f.connectAgent(agentDocker)
	plain := f.connectAgent(agentPlain)
	docker.serve(func(call protocol.ChannelFrame) protocol.ChannelFrame {
		return protocol.ChannelFrame{Result: json.RawMessage(`{"containers":[{"id":"abc","name":"web","image":"nginx","state":"running","status":"Up","created":1,"project":"site"}]}`)}
	})
	plain.serve(func(call protocol.ChannelFrame) protocol.ChannelFrame {
		return protocol.ChannelFrame{Error: &protocol.ChannelError{Code: protocol.ErrCodeUnknownMethod, Message: "unknown method"}}
	})

	if resp := f.get("/api/containers", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous fleet request returned %d", resp.StatusCode)
	}
	resp := f.get("/api/containers", auth.RoleViewer)
	var body struct{ Hosts []Host }
	decode(t, resp, &body)
	if resp.StatusCode != http.StatusOK || len(body.Hosts) != 3 {
		t.Fatalf("fleet = %d %+v", resp.StatusCode, body)
	}
	byID := map[string]Host{}
	for _, host := range body.Hosts {
		byID[host.AgentID] = host
	}
	if h := byID[agentDocker]; h.Name != "nas" || h.State != StateOK || len(h.Containers) != 1 || h.Containers[0].Project != "site" || h.DockerTier != "read" {
		t.Errorf("docker host = %+v", h)
	}
	if h := byID[agentPlain]; h.Name != "22222222" || h.State != StateUnavailable || !strings.Contains(h.Message, "VIGIRA_DOCKER_ENABLED") || h.Containers == nil {
		t.Errorf("plain host = %+v", h)
	}
	if h := byID[agentOffline]; h.Name != "attic" || h.State != StateOffline || h.Containers == nil || len(h.Containers) != 0 {
		t.Errorf("offline host = %+v", h)
	}
	for i := 1; i < len(body.Hosts); i++ {
		if body.Hosts[i-1].Name > body.Hosts[i].Name {
			t.Errorf("hosts are not sorted by name: %v then %v", body.Hosts[i-1].Name, body.Hosts[i].Name)
		}
	}
}

func TestInspectKeepsEnvironmentValuesBehindTheRevealPermission(t *testing.T) {
	f := newFixture(t)
	agent := f.connectAgent(agentDocker)
	var requested []protocol.DockerInspectParams
	agent.serve(func(call protocol.ChannelFrame) protocol.ChannelFrame {
		var params protocol.DockerInspectParams
		_ = json.Unmarshal(call.Params, &params)
		requested = append(requested, params)
		if params.ID == "gone" {
			return protocol.ChannelFrame{Error: &protocol.ChannelError{Code: protocol.ErrCodeNotFound, Message: "that container no longer exists"}}
		}
		return protocol.ChannelFrame{Result: json.RawMessage(`{"id":"abc","name":"web","env":[{"name":"TOKEN"}],"envRevealed":false}`)}
	})
	path := "/api/containers/" + agentDocker + "/web/inspect"

	if resp := f.get(path, ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous inspect returned %d", resp.StatusCode)
	}
	if resp := f.get(path, auth.RoleViewer); resp.StatusCode != http.StatusOK || len(requested) != 1 || requested[0].RevealEnv {
		t.Fatalf("viewer inspect = %d, agent saw %+v", resp.StatusCode, requested)
	}
	if resp := f.get(path+"?reveal=env", auth.RoleOperator); resp.StatusCode != http.StatusForbidden || len(requested) != 1 {
		t.Fatalf("operator reveal returned %d and reached the agent: %+v", resp.StatusCode, requested)
	}
	if resp := f.get(path+"?reveal=env", auth.RoleAdmin); resp.StatusCode != http.StatusOK || len(requested) != 2 || !requested[1].RevealEnv {
		t.Fatalf("admin reveal = %d, agent saw %+v", resp.StatusCode, requested)
	}
	f.audit.mu.Lock()
	events := append([]audit.Event(nil), f.audit.events...)
	f.audit.mu.Unlock()
	if len(events) != 2 || events[0].Outcome != audit.OutcomeDenied || events[0].ActorName != auth.RoleOperator || events[1].Outcome != audit.OutcomeSuccess || events[1].ActorName != auth.RoleAdmin ||
		events[1].Action != "container.env.reveal" || events[1].TargetID != "web" || events[1].AgentID != agentDocker || len(events[1].Detail) != 0 {
		t.Fatalf("audit events = %+v, want one denied and one successful reveal, without values", events)
	}
	if resp := f.get("/api/containers/"+agentDocker+"/gone/inspect", auth.RoleViewer); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing container returned %d", resp.StatusCode)
	}
	if resp := f.get("/api/containers/"+agentOffline+"/web/inspect", auth.RoleViewer); resp.StatusCode != http.StatusConflict {
		t.Fatalf("offline host returned %d", resp.StatusCode)
	}
	before := len(requested)
	for _, bad := range []string{"/api/containers/not-a-uuid/web/inspect", "/api/containers/" + agentDocker + "/-bad/inspect", "/api/containers/" + agentDocker + "/a%20b/inspect"} {
		if resp := f.get(bad, auth.RoleAdmin); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s returned %d, want 404", bad, resp.StatusCode)
		}
	}
	if len(requested) != before {
		t.Fatal("malformed references reached the agent")
	}
}

func (f *fixture) browser(path, role string, header http.Header) (*websocket.Conn, *http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if header == nil {
		header = http.Header{}
	}
	if role != "" {
		header.Set("Cookie", "role="+role)
	}
	return websocket.Dial(ctx, "ws"+strings.TrimPrefix(f.server.URL, "http")+path, &websocket.DialOptions{HTTPHeader: header})
}

func readText(t *testing.T, ws *websocket.Conn) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := ws.Read(ctx)
	if err != nil {
		t.Fatalf("browser read: %v", err)
	}
	return string(data)
}

func TestLogStreamsRelayToTheBrowserAndStopWhenItLeaves(t *testing.T) {
	f := newFixture(t)
	agent := f.connectAgent(agentDocker)

	if _, resp, err := f.browser("/api/containers/"+agentDocker+"/web/logs", "", nil); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous stream upgrade = %v %v", resp, err)
	}
	if _, resp, err := f.browser("/api/containers/"+agentDocker+"/web/logs", auth.RoleViewer, http.Header{"Origin": []string{"https://evil.example"}}); err == nil || resp == nil || (resp.StatusCode != http.StatusForbidden) {
		t.Fatalf("cross-origin upgrade = %v %v", resp, err)
	}

	ws, _, err := f.browser("/api/containers/"+agentDocker+"/web/logs?tail=50&follow=true&timestamps=true", auth.RoleViewer, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	open := agent.recv()
	var params protocol.DockerLogsParams
	if open.Type != protocol.FrameStreamOpen || open.Method != protocol.MethodDockerLogs || json.Unmarshal(open.Params, &params) != nil ||
		params.ID != "web" || params.Tail != 50 || !params.Follow || !params.Timestamps {
		t.Fatalf("agent saw %+v params %+v", open, params)
	}
	agent.send(protocol.ChannelFrame{Type: protocol.FrameStreamData, ID: open.ID, Seq: 1, Data: []byte(`{"s":"o","m":"hello"}` + "\n")})
	if got := readText(t, ws); !strings.Contains(got, `"m":"hello"`) {
		t.Fatalf("browser got %q", got)
	}
	agent.send(protocol.ChannelFrame{Type: protocol.FrameStreamEnd, ID: open.ID})
	if got := readText(t, ws); got != `{"end":true}` {
		t.Fatalf("end message = %q", got)
	}

	// Leaving the page cancels the agent's stream.
	ws2, _, err := f.browser("/api/containers/"+agentDocker+"/web/stats", auth.RoleViewer, nil)
	if err != nil {
		t.Fatal(err)
	}
	open = agent.recv()
	if open.Method != protocol.MethodDockerStats {
		t.Fatalf("second stream = %+v", open)
	}
	ws2.CloseNow()
	if frame := agent.recv(); frame.Type != protocol.FrameCancel || frame.ID != open.ID {
		t.Fatalf("expected cancel for %s, got %+v", open.ID, frame)
	}
}

func TestStreamErrorsReachTheBrowserAsAnEndMessage(t *testing.T) {
	f := newFixture(t)
	agent := f.connectAgent(agentDocker)

	// An offline host and a host failing mid-stream both end with a readable reason.
	ws, _, err := f.browser("/api/containers/"+agentOffline+"/web/logs", auth.RoleViewer, nil)
	if err != nil {
		t.Fatal(err)
	}
	var end endMessage
	if err := json.Unmarshal([]byte(readText(t, ws)), &end); err != nil || !end.End || end.Message != "This host is not connected." {
		t.Fatalf("offline end = %+v, %v", end, err)
	}
	ws.CloseNow()

	ws, _, err = f.browser("/api/containers/"+agentDocker+"/web/logs", auth.RoleViewer, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	open := agent.recv()
	agent.send(protocol.ChannelFrame{Type: protocol.FrameStreamEnd, ID: open.ID, Error: &protocol.ChannelError{Code: protocol.ErrCodeNotFound, Message: "that container no longer exists"}})
	end = endMessage{}
	if err := json.Unmarshal([]byte(readText(t, ws)), &end); err != nil || !end.End || end.Code != protocol.ErrCodeNotFound || end.Message != "That container no longer exists." {
		t.Fatalf("mid-stream end = %+v, %v", end, err)
	}
}
