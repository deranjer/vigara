package containers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/deranjer/vigara/apps/api/internal/audit"
	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/internal/protocol"
)

func TestDeviceShellRequiresPermissionAndSeparateHostOptIn(t *testing.T) {
	f := newFixture(t)
	f.connectAgent(agentExec)
	path := "/api/containers/" + agentExec + "/shell"
	for _, test := range []struct {
		role, query string
		status      int
	}{
		{"", "", 401}, {auth.RoleViewer, "", 403},
		{auth.RoleOperator, "?shell=sh%20-c%20id", 400},
		{auth.RoleOperator, "?user=root", 400},
	} {
		ws, resp, err := f.browser(path+test.query, test.role, nil)
		if ws != nil {
			ws.CloseNow()
		}
		if err == nil || resp == nil || resp.StatusCode != test.status {
			t.Fatalf("%+v = %v %v", test, resp, err)
		}
	}
	if _, resp, err := f.browser(path, auth.RoleAdmin, http.Header{"Origin": {"https://other.example"}}); err == nil || resp.StatusCode != 403 {
		t.Fatalf("origin: %v %v", resp, err)
	}
	ws, _, err := f.browser(path, auth.RoleAdmin, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	end := readEnd(t, ws)
	if end.Code != protocol.ErrCodeTierInsufficient || !strings.Contains(end.Message, "Device shells") {
		t.Fatalf("Docker exec must not grant host exec: %+v", end)
	}
}

func TestDeviceShellRelaysWithoutDockerAndAuditsDisconnect(t *testing.T) {
	f := newFixture(t)
	agent := f.connectAgent(agentShell)
	ws, _, err := f.browser("/api/containers/"+agentShell+"/shell?shell=powershell&cols=120&rows=35", auth.RoleOperator, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	open := agent.recv()
	var p protocol.HostExecParams
	if open.Method != protocol.MethodHostExec || json.Unmarshal(open.Params, &p) != nil || p.Shell != "powershell" || p.Cols != 120 || p.Rows != 35 {
		t.Fatalf("open = %+v", open)
	}
	agent.send(protocol.ChannelFrame{Type: protocol.FrameStreamData, ID: open.ID, Seq: 1, Data: []byte("PS> ")})
	if kind, data := readBrowser(t, ws); kind != websocket.MessageBinary || data != "PS> " {
		t.Fatalf("output = %v %q", kind, data)
	}
	writeBrowser(t, ws, websocket.MessageBinary, []byte("whoami\r"))
	if frame := agent.recv(); frame.Type != protocol.FrameStreamInput || string(frame.Data) != "whoami\r" {
		t.Fatalf("input = %+v", frame)
	}
	writeBrowser(t, ws, websocket.MessageText, []byte(`{"type":"resize","cols":140,"rows":40}`))
	if frame := agent.recv(); frame.Type != protocol.FrameStreamControl {
		t.Fatalf("resize = %+v", frame)
	}
	ws.CloseNow()
	if frame := agent.recv(); frame.Type != protocol.FrameCancel {
		t.Fatalf("disconnect = %+v", frame)
	}
	event := waitEvent(t, f, "device.exec.close")
	if event.AgentID != agentShell || event.TargetKind != "agent" || event.Detail["reason"] != reasonClientLeft {
		t.Fatalf("close = %+v", event)
	}
	if event := waitEvent(t, f, "device.exec.open"); event.Outcome != audit.OutcomeSuccess {
		t.Fatalf("open = %+v", event)
	}
	for _, event := range f.events() {
		for _, value := range event.Detail {
			if strings.Contains(fmt.Sprint(value), "whoami") {
				t.Fatal("terminal content leaked to audit")
			}
		}
	}
}

func TestDeviceShellIdleTimeoutCancelsAgent(t *testing.T) {
	setExecLimits(t, 60*time.Millisecond, time.Second)
	f := newFixture(t)
	agent := f.connectAgent(agentShell)
	ws, _, err := f.browser("/api/containers/"+agentShell+"/shell", auth.RoleAdmin, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	agent.recv()
	if end := readEnd(t, ws); end.Code != reasonIdle {
		t.Fatalf("end = %+v", end)
	}
	if frame := agent.recv(); frame.Type != protocol.FrameCancel {
		t.Fatalf("idle cancel = %+v", frame)
	}
}
