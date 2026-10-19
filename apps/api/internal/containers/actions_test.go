package containers

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/audit"
	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/internal/protocol"
)

func (f *fixture) post(path, role, body string, header http.Header) *http.Response {
	f.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, f.server.URL+path, strings.NewReader(body))
	for key, values := range header {
		req.Header[key] = values
	}
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

func (f *fixture) events() []audit.Event {
	f.audit.mu.Lock()
	defer f.audit.mu.Unlock()
	return append([]audit.Event(nil), f.audit.events...)
}

func bodyText(resp *http.Response) string {
	var sb strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		sb.WriteString(scanner.Text() + "\n")
	}
	return strings.TrimSpace(sb.String())
}

func TestActionsNeedTheManagePermissionAndAreAudited(t *testing.T) {
	f := newFixture(t)
	agent := f.connectAgent(agentManage)
	var received []protocol.DockerActionParams
	agent.serve(func(call protocol.ChannelFrame) protocol.ChannelFrame {
		var params protocol.DockerActionParams
		_ = json.Unmarshal(call.Params, &params)
		received = append(received, params)
		if params.ID == "busy" {
			return protocol.ChannelFrame{Error: &protocol.ChannelError{Code: protocol.ErrCodeConflict, Message: "You cannot remove a running container. Stop it first."}}
		}
		return protocol.ChannelFrame{Result: json.RawMessage(`{"id":"abc","name":"web","action":"` + params.Action + `","changed":true}`)}
	})
	path := func(id string) string { return "/api/containers/" + agentManage + "/" + id + "/actions" }

	if resp := f.post(path("web"), "", `{"action":"restart"}`, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d", resp.StatusCode)
	}
	if resp := f.post(path("web"), auth.RoleViewer, `{"action":"restart"}`, nil); resp.StatusCode != http.StatusForbidden || len(received) != 0 {
		t.Fatalf("viewer = %d, agent saw %v", resp.StatusCode, received)
	}
	if resp := f.post(path("web"), auth.RoleOperator, `{"action":"restart"}`, http.Header{"Origin": []string{"https://evil.example"}}); resp.StatusCode != http.StatusForbidden || len(received) != 0 {
		t.Fatalf("cross-origin = %d, agent saw %v", resp.StatusCode, received)
	}
	resp := f.post(path("web"), auth.RoleOperator, `{"action":"restart","timeoutSeconds":20}`, nil)
	var result protocol.DockerActionResult
	if err := json.NewDecoder(resp.Body).Decode(&result); resp.StatusCode != http.StatusOK || err != nil || result.Name != "web" || !result.Changed {
		t.Fatalf("operator restart = %d %+v %v", resp.StatusCode, result, err)
	}
	if len(received) != 1 || received[0].ID != "web" || received[0].Action != "restart" || received[0].TimeoutSeconds != 20 {
		t.Fatalf("agent saw %+v (the container comes from the path, never the body)", received)
	}
	if resp := f.post(path("web"), auth.RoleAdmin, `{"id":"other","action":"remove","force":true,"removeVolumes":true}`, nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a body naming another container must be refused, got %d", resp.StatusCode)
	}
	if resp := f.post(path("web"), auth.RoleAdmin, `{"action":"remove","force":true,"removeVolumes":true}`, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("admin remove = %d", resp.StatusCode)
	}
	if resp := f.post(path("busy"), auth.RoleOperator, `{"action":"remove"}`, nil); resp.StatusCode != http.StatusConflict || !strings.Contains(bodyText(resp), "running container") {
		t.Fatalf("a Docker conflict should reach the user")
	}
	for _, bad := range []string{`{"action":"exec"}`, `{"action":""}`, `{}`, `not json`, `{"action":"stop"} {"action":"remove"}`} {
		before := len(received)
		if resp := f.post(path("web"), auth.RoleOperator, bad, nil); resp.StatusCode != http.StatusBadRequest || len(received) != before {
			t.Errorf("%q = %d, agent asked: %v", bad, resp.StatusCode, len(received) != before)
		}
	}
	if resp := f.get(path("web"), auth.RoleOperator); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET on an action route = %d", resp.StatusCode)
	}

	byOutcome := map[string][]audit.Event{}
	for _, event := range f.events() {
		byOutcome[event.Outcome] = append(byOutcome[event.Outcome], event)
	}
	denied := byOutcome[audit.OutcomeDenied]
	if len(denied) != 1 || denied[0].Action != "container.restart" || denied[0].ActorName != auth.RoleViewer || denied[0].TargetID != "web" || denied[0].AgentID != agentManage {
		t.Fatalf("denied = %+v", denied)
	}
	success := byOutcome[audit.OutcomeSuccess]
	if len(success) != 2 || success[0].Action != "container.restart" || success[0].Detail["name"] != "web" || success[1].Action != "container.remove" || success[1].Detail["force"] != true || success[1].Detail["removeVolumes"] != true {
		t.Fatalf("success = %+v", success)
	}
	failure := byOutcome[audit.OutcomeFailure]
	if len(failure) != 1 || failure[0].Detail["code"] != protocol.ErrCodeConflict {
		t.Fatalf("failure = %+v", failure)
	}
}

func TestActionsAreRefusedOnHostsWithoutTheTierOrOffline(t *testing.T) {
	f := newFixture(t)
	read := f.connectAgent(agentDocker) // enrolled for read-only access
	asked := false
	read.serve(func(protocol.ChannelFrame) protocol.ChannelFrame { asked = true; return protocol.ChannelFrame{} })

	resp := f.post("/api/containers/"+agentDocker+"/web/actions", auth.RoleAdmin, `{"action":"stop"}`, nil)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(bodyText(resp), "not enabled") || asked {
		t.Fatalf("read-only host = %d, agent asked: %v", resp.StatusCode, asked)
	}
	if resp := f.post("/api/containers/"+agentOffline+"/web/actions", auth.RoleAdmin, `{"action":"stop"}`, nil); resp.StatusCode != http.StatusConflict {
		t.Fatalf("offline host = %d", resp.StatusCode)
	}
	for _, bad := range []string{"/api/containers/not-a-uuid/web/actions", "/api/containers/" + agentManage + "/-x/actions", "/api/containers/" + agentManage + "/a%20b/actions"} {
		if resp := f.post(bad, auth.RoleAdmin, `{"action":"stop"}`, nil); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", bad, resp.StatusCode)
		}
	}
	failures := 0
	for _, event := range f.events() {
		if event.Outcome == audit.OutcomeFailure && event.Detail["code"] != nil {
			failures++
		}
	}
	if failures != 2 {
		t.Fatalf("expected the refused and offline attempts to be audited as failures, got %+v", f.events())
	}
}

func TestRecreateStreamsProgressAndIsAudited(t *testing.T) {
	f := newFixture(t)
	agent := f.connectAgent(agentManage)
	path := "/api/containers/" + agentManage + "/web/recreate"

	if resp := f.post(path, auth.RoleViewer, `{"pull":true}`, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer recreate = %d", resp.StatusCode)
	}

	type outcome struct {
		resp *http.Response
		body string
	}
	finished := make(chan outcome, 1)
	go func() {
		resp := f.post(path, auth.RoleOperator, `{"pull":true}`, nil)
		finished <- outcome{resp, bodyText(resp)}
	}()
	open := agent.recv()
	var params protocol.DockerRecreateParams
	if open.Type != protocol.FrameStreamOpen || open.Method != protocol.MethodDockerRecreate || json.Unmarshal(open.Params, &params) != nil || params.ID != "web" || !params.Pull {
		t.Fatalf("agent saw %+v %+v", open, params)
	}
	agent.send(protocol.ChannelFrame{Type: protocol.FrameStreamData, ID: open.ID, Seq: 1, Data: []byte(`{"step":"pull","message":"Pulling app:1."}` + "\n")})
	agent.send(protocol.ChannelFrame{Type: protocol.FrameStreamData, ID: open.ID, Seq: 2, Data: []byte(`{"step":"done","result":{"id":"new","name":"web","imageChanged":true,"started":true}}` + "\n")})
	agent.send(protocol.ChannelFrame{Type: protocol.FrameStreamEnd, ID: open.ID})
	got := <-finished
	if got.resp.StatusCode != http.StatusOK || got.resp.Header.Get("Content-Type") != "application/x-ndjson" ||
		!strings.Contains(got.body, `"step":"pull"`) || !strings.Contains(got.body, `"step":"done"`) {
		t.Fatalf("response = %d %q", got.resp.StatusCode, got.body)
	}
	events := f.events()
	if len(events) != 2 || events[0].Outcome != audit.OutcomeDenied || events[1].Outcome != audit.OutcomeSuccess || events[1].Action != "container.recreate" ||
		events[1].Detail["name"] != "web" || events[1].Detail["imageChanged"] != true || events[1].Detail["pull"] != true {
		t.Fatalf("audit = %+v", events)
	}

	// A failed recreate ends the stream with an error line and an audited failure.
	go func() {
		resp := f.post(path, auth.RoleAdmin, ``, nil)
		finished <- outcome{resp, bodyText(resp)}
	}()
	open = agent.recv()
	agent.send(protocol.ChannelFrame{Type: protocol.FrameStreamEnd, ID: open.ID, Error: &protocol.ChannelError{Code: protocol.ErrCodeRecreateFailed, Message: "recreate failed during start (the original container was restored): port is already allocated"}})
	got = <-finished
	if !strings.Contains(got.body, `"step":"error"`) || !strings.Contains(got.body, "the original container was restored") || !strings.Contains(got.body, protocol.ErrCodeRecreateFailed) {
		t.Fatalf("failure body = %q", got.body)
	}
	events = f.events()
	if last := events[len(events)-1]; last.Outcome != audit.OutcomeFailure || last.Detail["code"] != protocol.ErrCodeRecreateFailed || last.Detail["pull"] != false {
		t.Fatalf("failure audit = %+v", last)
	}

	// An offline host fails before streaming starts, with a proper status.
	if resp := f.post("/api/containers/"+agentOffline+"/web/recreate", auth.RoleAdmin, `{}`, nil); resp.StatusCode != http.StatusConflict {
		t.Fatalf("offline recreate = %d", resp.StatusCode)
	}
}

func TestLeavingDuringARecreateCancelsTheAgentsStream(t *testing.T) {
	f := newFixture(t)
	agent := f.connectAgent(agentManage)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, f.server.URL+"/api/containers/"+agentManage+"/web/recreate", strings.NewReader(`{"pull":true}`))
	req.AddCookie(&http.Cookie{Name: "role", Value: auth.RoleOperator})
	gotLine := make(chan struct{})
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		if _, err := bufio.NewReader(resp.Body).ReadString('\n'); err == nil {
			close(gotLine)
		}
	}()
	open := agent.recv()
	agent.send(protocol.ChannelFrame{Type: protocol.FrameStreamData, ID: open.ID, Seq: 1, Data: []byte(`{"step":"pull","message":"Pulling."}` + "\n")})
	select {
	case <-gotLine:
	case <-time.After(5 * time.Second):
		t.Fatal("the browser never received progress")
	}
	// The agent now sends nothing more, as during a long silent step; leaving must still be noticed.
	cancel()
	if frame := agent.recv(); frame.Type != protocol.FrameCancel || frame.ID != open.ID {
		t.Fatalf("expected cancel for %s, got %+v", open.ID, frame)
	}
	var last audit.Event
	for i := 0; i < 100; i++ {
		if events := f.events(); len(events) > 0 {
			last = events[len(events)-1]
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if last.Action != "container.recreate" || last.Outcome != audit.OutcomeFailure || last.Detail["code"] != "client_left" {
		t.Fatalf("a recreate the browser walked away from must be audited as unknown, got %+v", last)
	}
}
