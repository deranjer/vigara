package containers

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/deranjer/vigara/apps/api/internal/audit"
	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/internal/protocol"
)

func TestHostRunNeedsExecPermissionValidatesAndAudits(t *testing.T) {
	f := newFixture(t)
	agent := f.connectAgent(agentShell)
	var received []protocol.HostRunParams
	agent.serve(func(call protocol.ChannelFrame) protocol.ChannelFrame {
		var params protocol.HostRunParams
		_ = json.Unmarshal(call.Params, &params)
		received = append(received, params)
		return protocol.ChannelFrame{Result: json.RawMessage(`{"exitCode":0,"stdout":"SECRET-OUTPUT"}`)}
	})
	path := "/api/containers/" + agentShell + "/host-run"
	if resp := f.post(path, "", `{"command":"df"}`, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d", resp.StatusCode)
	}
	if resp := f.post(path, auth.RoleViewer, `{"command":"df"}`, nil); resp.StatusCode != http.StatusForbidden || len(received) != 0 {
		t.Fatalf("viewer = %d", resp.StatusCode)
	}
	for _, body := range []string{`{"command":"cat","path":"/etc/shadow"}`, `{"command":"ls","path":"/a/../b"}`, `{"command":"df","shell":"sh"}`, `{"command":"ls"}`} {
		if resp := f.post(path, auth.RoleOperator, body, nil); resp.StatusCode != http.StatusBadRequest || len(received) != 0 {
			t.Fatalf("%s = %d", body, resp.StatusCode)
		}
	}
	resp := f.post(path, auth.RoleOperator, `{"command":"ls","path":"/srv/stacks"}`, nil)
	var result protocol.HostRunResult
	if err := json.NewDecoder(resp.Body).Decode(&result); resp.StatusCode != http.StatusOK || err != nil || result.Stdout != "SECRET-OUTPUT" {
		t.Fatalf("operator = %d %+v %v", resp.StatusCode, result, err)
	}
	if len(received) != 1 || received[0].Command != "ls" || received[0].Path != "/srv/stacks" {
		t.Fatalf("agent saw %+v", received)
	}
	var sawDenied, sawRun bool
	for _, event := range f.events() {
		if event.Action != "device.run" {
			continue
		}
		sawDenied = sawDenied || event.Outcome == audit.OutcomeDenied
		if event.Outcome == audit.OutcomeSuccess {
			sawRun = event.Detail["command"] == "ls" && event.Detail["path"] == "/srv/stacks"
		}
		for _, value := range event.Detail {
			if value == "SECRET-OUTPUT" {
				t.Fatal("command output leaked to audit")
			}
		}
	}
	if !sawDenied || !sawRun {
		t.Fatalf("audit incomplete: denied=%v run=%v", sawDenied, sawRun)
	}
}

func TestHostRunRefusedWithoutHostOptIn(t *testing.T) {
	f := newFixture(t)
	f.connectAgent(agentExec) // Docker exec only; no device shell opt-in
	resp := f.post("/api/containers/"+agentExec+"/host-run", auth.RoleAdmin, `{"command":"df"}`, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d: %s", resp.StatusCode, bodyText(resp))
	}
}
