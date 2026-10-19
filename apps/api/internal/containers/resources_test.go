package containers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/audit"
	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/internal/protocol"
)

func dockerPath(agent, rest string) string { return "/api/docker/" + agent + "/" + rest }

// recordingAgent answers resource calls and remembers what it was asked.
type recordingAgent struct {
	mu     sync.Mutex
	asked  []protocol.ChannelFrame
	answer func(call protocol.ChannelFrame) protocol.ChannelFrame
}

func (r *recordingAgent) calls() []protocol.ChannelFrame {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]protocol.ChannelFrame(nil), r.asked...)
}

func startRecording(a *agentEnd, answer func(call protocol.ChannelFrame) protocol.ChannelFrame) *recordingAgent {
	rec := &recordingAgent{answer: answer}
	a.serve(func(call protocol.ChannelFrame) protocol.ChannelFrame {
		rec.mu.Lock()
		rec.asked = append(rec.asked, call)
		rec.mu.Unlock()
		return rec.answer(call)
	})
	return rec
}

func TestResourceListsAndInspectNeedOnlyReadAccess(t *testing.T) {
	f := newFixture(t)
	agent := f.connectAgent(agentDocker)
	rec := startRecording(agent, func(call protocol.ChannelFrame) protocol.ChannelFrame {
		switch call.Method {
		case protocol.MethodDockerImages:
			return protocol.ChannelFrame{Result: json.RawMessage(`{"images":[{"id":"sha256:a","tags":["app:1"],"created":5,"size":10,"dangling":false}]}`)}
		case protocol.MethodDockerResourceInspect:
			return protocol.ChannelFrame{Result: json.RawMessage(`{"kind":"volume","volume":{"name":"pgdata","size":1}}`)}
		}
		return protocol.ChannelFrame{Error: &protocol.ChannelError{Code: protocol.ErrCodeDockerDown, Message: "Docker could not be reached"}}
	})

	if resp := f.get(dockerPath(agentDocker, "images"), ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d", resp.StatusCode)
	}
	resp := f.get(dockerPath(agentDocker, "images"), auth.RoleViewer)
	var images protocol.DockerImagesResult
	if err := json.NewDecoder(resp.Body).Decode(&images); resp.StatusCode != http.StatusOK || err != nil || len(images.Images) != 1 || images.Images[0].Tags[0] != "app:1" {
		t.Fatalf("viewer image list = %d %+v %v", resp.StatusCode, images, err)
	}
	resp = f.get(dockerPath(agentDocker, "volumes/inspect?ref=pgdata"), auth.RoleViewer)
	var inspect protocol.DockerResourceInspectResult
	if err := json.NewDecoder(resp.Body).Decode(&inspect); resp.StatusCode != http.StatusOK || err != nil || inspect.Volume == nil || inspect.Volume.Name != "pgdata" {
		t.Fatalf("viewer volume inspect = %d %+v %v", resp.StatusCode, inspect, err)
	}
	var sent protocol.DockerResourceParams
	for _, call := range rec.calls() {
		if call.Method == protocol.MethodDockerResourceInspect {
			_ = json.Unmarshal(call.Params, &sent)
		}
	}
	if sent.Kind != protocol.KindVolume || sent.ID != "pgdata" || sent.Force {
		t.Fatalf("the agent was asked %+v", sent)
	}

	before := len(rec.calls())
	for _, bad := range []string{dockerPath("not-a-uuid", "images"), dockerPath(agentDocker, "secrets"), dockerPath(agentDocker, "images/inspect?ref=../etc"), dockerPath(agentDocker, "volumes/inspect?ref=a%2Fb"), dockerPath(agentDocker, "networks/inspect")} {
		if resp := f.get(bad, auth.RoleViewer); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", bad, resp.StatusCode)
		}
	}
	if len(rec.calls()) != before {
		t.Fatal("a malformed request reached the host")
	}
	if resp := f.get(dockerPath(agentOffline, "images"), auth.RoleViewer); resp.StatusCode != http.StatusConflict {
		t.Fatalf("offline host = %d", resp.StatusCode)
	}
	if resp := f.get(dockerPath(agentDocker, "networks"), auth.RoleViewer); resp.StatusCode != http.StatusBadGateway || !strings.Contains(bodyText(resp), "Docker could not be reached") {
		t.Fatalf("Docker being down must be explained: %d", resp.StatusCode)
	}
}

func TestRemovingResourcesNeedsManagePermissionAndIsAudited(t *testing.T) {
	f := newFixture(t)
	agent := f.connectAgent(agentManage)
	rec := startRecording(agent, func(call protocol.ChannelFrame) protocol.ChannelFrame {
		var params protocol.DockerResourceParams
		_ = json.Unmarshal(call.Params, &params)
		if params.ID == "bridge" {
			return protocol.ChannelFrame{Error: &protocol.ChannelError{Code: protocol.ErrCodeConflict, Message: "bridge is a pre-defined network and cannot be removed"}}
		}
		return protocol.ChannelFrame{Result: json.RawMessage(`{"kind":"` + params.Kind + `","id":"` + params.ID + `","untagged":["app:1"],"deleted":["sha256:a","sha256:b"]}`)}
	})
	path := func(kind string) string { return dockerPath(agentManage, kind+"/remove") }

	if resp := f.post(path("images"), "", `{"ref":"app:1"}`, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d", resp.StatusCode)
	}
	if resp := f.post(path("volumes"), auth.RoleViewer, `{"ref":"pgdata"}`, nil); resp.StatusCode != http.StatusForbidden || len(rec.calls()) != 0 {
		t.Fatalf("viewer = %d, agent asked %d times", resp.StatusCode, len(rec.calls()))
	}
	if resp := f.post(path("images"), auth.RoleOperator, `{"ref":"app:1"}`, http.Header{"Origin": []string{"https://evil.example"}}); resp.StatusCode != http.StatusForbidden || len(rec.calls()) != 0 {
		t.Fatalf("cross-origin = %d", resp.StatusCode)
	}
	resp := f.post(path("images"), auth.RoleOperator, `{"ref":"app:1","force":true}`, nil)
	var removed protocol.DockerRemoveResult
	if err := json.NewDecoder(resp.Body).Decode(&removed); resp.StatusCode != http.StatusOK || err != nil || len(removed.Untagged) != 1 || len(removed.Deleted) != 2 {
		t.Fatalf("operator image removal = %d %+v %v", resp.StatusCode, removed, err)
	}
	if resp := f.post(path("networks"), auth.RoleAdmin, `{"ref":"bridge"}`, nil); resp.StatusCode != http.StatusConflict || !strings.Contains(bodyText(resp), "pre-defined network") {
		t.Fatalf("Docker's refusal must reach the user: %d", resp.StatusCode)
	}
	for _, bad := range []string{`{"ref":"../x"}`, `{"ref":""}`, `{"ref":"a b"}`, `{"ref":"x","extra":1}`, `{}`, `nope`} {
		before := len(rec.calls())
		if resp := f.post(path("volumes"), auth.RoleOperator, bad, nil); resp.StatusCode != http.StatusBadRequest || len(rec.calls()) != before {
			t.Errorf("%s = %d", bad, resp.StatusCode)
		}
	}

	var sent protocol.DockerResourceParams
	_ = json.Unmarshal(rec.calls()[0].Params, &sent)
	if sent.Kind != protocol.KindImage || sent.ID != "app:1" || !sent.Force {
		t.Fatalf("the agent was asked %+v", sent)
	}
	byOutcome := map[string][]audit.Event{}
	for _, event := range f.events() {
		byOutcome[event.Outcome] = append(byOutcome[event.Outcome], event)
	}
	if d := byOutcome[audit.OutcomeDenied]; len(d) != 1 || d[0].Action != "volume.remove" || d[0].TargetKind != "volume" || d[0].TargetID != "pgdata" {
		t.Fatalf("denied = %+v (only the viewer is a role denial, and it must be audited against its own target; the cross-origin request is rejected earlier)", d)
	}
	if s := byOutcome[audit.OutcomeSuccess]; len(s) != 1 || s[0].Action != "image.remove" || s[0].TargetKind != "image" || s[0].TargetID != "app:1" || s[0].Detail["force"] != true || s[0].Detail["deleted"] != 2 {
		t.Fatalf("success = %+v", s)
	}
	if fl := byOutcome[audit.OutcomeFailure]; len(fl) != 1 || fl[0].Action != "network.remove" || fl[0].Detail["code"] != protocol.ErrCodeConflict {
		t.Fatalf("failure = %+v", fl)
	}
}

func TestRemovalIsRefusedOnHostsBelowTheManageTier(t *testing.T) {
	f := newFixture(t)
	agent := f.connectAgent(agentDocker) // read-only enrollment
	asked := false
	agent.serve(func(protocol.ChannelFrame) protocol.ChannelFrame { asked = true; return protocol.ChannelFrame{} })
	resp := f.post(dockerPath(agentDocker, "volumes/remove"), auth.RoleAdmin, `{"ref":"pgdata"}`, nil)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(bodyText(resp), "not enabled") || asked {
		t.Fatalf("read-only host = %d, asked %v", resp.StatusCode, asked)
	}
}

func TestPruneDryRunsAreNotAuditedButRealPrunesAre(t *testing.T) {
	f := newFixture(t)
	agent := f.connectAgent(agentManage)
	rec := startRecording(agent, func(call protocol.ChannelFrame) protocol.ChannelFrame {
		var params protocol.DockerPruneParams
		_ = json.Unmarshal(call.Params, &params)
		if params.DryRun {
			return protocol.ChannelFrame{Result: json.RawMessage(`{"kind":"volume","dryRun":true,"candidates":[{"id":"cache-old","name":"cache-old","size":2048}]}`)}
		}
		return protocol.ChannelFrame{Result: json.RawMessage(`{"kind":"volume","dryRun":false,"removed":["cache-old"],"spaceReclaimed":2048}`)}
	})
	path := dockerPath(agentManage, "volumes/prune")
	if resp := f.post(path, auth.RoleViewer, `{"dryRun":true}`, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer = %d", resp.StatusCode)
	}
	var preview protocol.DockerPruneResult
	resp := f.post(path, auth.RoleOperator, `{"all":true,"dryRun":true}`, nil)
	if err := json.NewDecoder(resp.Body).Decode(&preview); resp.StatusCode != http.StatusOK || err != nil || !preview.DryRun || len(preview.Candidates) != 1 {
		t.Fatalf("preview = %d %+v %v", resp.StatusCode, preview, err)
	}
	for _, event := range f.events() {
		if event.Outcome != audit.OutcomeDenied {
			t.Fatalf("a dry run was audited: %+v", event)
		}
	}
	var done protocol.DockerPruneResult
	resp = f.post(path, auth.RoleOperator, `{"all":true}`, nil)
	if err := json.NewDecoder(resp.Body).Decode(&done); resp.StatusCode != http.StatusOK || err != nil || done.DryRun || done.SpaceReclaimed != 2048 {
		t.Fatalf("prune = %d %+v %v", resp.StatusCode, done, err)
	}
	var sent protocol.DockerPruneParams
	calls := rec.calls()
	_ = json.Unmarshal(calls[len(calls)-1].Params, &sent)
	if sent.Kind != protocol.KindVolume || !sent.All || sent.DryRun {
		t.Fatalf("the agent was asked %+v", sent)
	}
	last := f.events()[len(f.events())-1]
	if last.Action != "volume.prune" || last.Outcome != audit.OutcomeSuccess || last.Detail["all"] != true || last.Detail["removed"] != 1 || last.Detail["spaceReclaimed"] != int64(2048) {
		t.Fatalf("prune audit = %+v", last)
	}
}

func TestPullingStreamsProgressAndIsAudited(t *testing.T) {
	f := newFixture(t)
	agent := f.connectAgent(agentManage)
	path := dockerPath(agentManage, "images/pull")

	if resp := f.post(path, auth.RoleViewer, `{"ref":"redis:7"}`, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer = %d", resp.StatusCode)
	}
	for _, bad := range []string{`{"ref":"sha256:0123456789ab"}`, `{"ref":"a b"}`, `{"ref":""}`, `{"ref":"-x"}`, `{"ref":"x","extra":1}`} {
		if resp := f.post(path, auth.RoleOperator, bad, nil); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", bad, resp.StatusCode)
		}
	}

	finished := make(chan string, 1)
	go func() {
		resp := f.post(path, auth.RoleOperator, `{"ref":"  redis:7 "}`, nil)
		finished <- bodyText(resp)
	}()
	open := agent.recv()
	var params protocol.DockerPullParams
	if open.Method != protocol.MethodDockerPull || json.Unmarshal(open.Params, &params) != nil || params.Ref != "redis:7" {
		t.Fatalf("agent saw %+v %+v", open, params)
	}
	agent.send(protocol.ChannelFrame{Type: protocol.FrameStreamData, ID: open.ID, Seq: 1, Data: []byte(`{"step":"pull","message":"Pulling redis:7."}` + "\n")})
	agent.send(protocol.ChannelFrame{Type: protocol.FrameStreamData, ID: open.ID, Seq: 2, Data: []byte(`{"step":"done","message":"Pulled redis:7."}` + "\n")})
	agent.send(protocol.ChannelFrame{Type: protocol.FrameStreamEnd, ID: open.ID})
	if body := <-finished; !strings.Contains(body, `"step":"pull"`) || !strings.Contains(body, `"step":"done"`) {
		t.Fatalf("body = %q", body)
	}
	pulled := waitEvent(t, f, "image.pull")
	if pulled.Outcome != audit.OutcomeSuccess || pulled.TargetKind != "image" || pulled.TargetID != "redis:7" {
		t.Fatalf("pull audit = %+v", pulled)
	}

	// A registry refusal ends the stream with the registry's own words, and is audited.
	go func() {
		resp := f.post(path, auth.RoleAdmin, `{"ref":"private/app:1"}`, nil)
		finished <- bodyText(resp)
	}()
	open = agent.recv()
	agent.send(protocol.ChannelFrame{Type: protocol.FrameStreamEnd, ID: open.ID, Error: &protocol.ChannelError{Code: protocol.ErrCodePullFailed, Message: "pull access denied for private/app"}})
	if body := <-finished; !strings.Contains(body, `"step":"error"`) || !strings.Contains(body, "pull access denied") {
		t.Fatalf("failure body = %q", body)
	}
	events := f.events()
	if last := events[len(events)-1]; last.Outcome != audit.OutcomeFailure || last.TargetID != "private/app:1" || last.Detail["code"] != protocol.ErrCodePullFailed {
		t.Fatalf("failed pull audit = %+v", last)
	}
	if resp := f.post(dockerPath(agentOffline, "images/pull"), auth.RoleAdmin, `{"ref":"redis:7"}`, nil); resp.StatusCode != http.StatusConflict {
		t.Fatalf("offline host = %d", resp.StatusCode)
	}
}

func TestLeavingDuringAPullCancelsIt(t *testing.T) {
	f := newFixture(t)
	agent := f.connectAgent(agentManage)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, f.server.URL+dockerPath(agentManage, "images/pull"), strings.NewReader(`{"ref":"redis:7"}`))
	req.AddCookie(&http.Cookie{Name: "role", Value: auth.RoleOperator})
	got := make(chan struct{})
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		buf := make([]byte, 64)
		if n, _ := resp.Body.Read(buf); n > 0 {
			close(got)
		}
	}()
	open := agent.recv()
	agent.send(protocol.ChannelFrame{Type: protocol.FrameStreamData, ID: open.ID, Seq: 1, Data: []byte(`{"step":"pull","message":"Pulling."}` + "\n")})
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("no progress reached the browser")
	}
	cancel()
	if frame := agent.recv(); frame.Type != protocol.FrameCancel || frame.ID != open.ID {
		t.Fatalf("expected cancel, got %+v", frame)
	}
	if event := waitEvent(t, f, "image.pull"); event.Detail["code"] != "client_left" || event.Outcome != audit.OutcomeFailure {
		t.Fatalf("audit = %+v", event)
	}
}
