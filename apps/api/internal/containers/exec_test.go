package containers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/deranjer/vigara/apps/api/internal/audit"
	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/internal/protocol"
)

func execPath(agent, id, query string) string {
	return "/api/containers/" + agent + "/" + id + "/exec" + query
}

// setExecLimits must run before newFixture, which reads the limits when it builds the API.
func setExecLimits(t *testing.T, idle, max time.Duration) {
	execIdle, execMax = idle, max
	t.Cleanup(func() { execIdle, execMax = 0, 0 })
}

func readBrowser(t *testing.T, ws *websocket.Conn) (websocket.MessageType, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	kind, data, err := ws.Read(ctx)
	if err != nil {
		t.Fatalf("browser read: %v", err)
	}
	return kind, string(data)
}

// readEnd reads until the terminal's final text message, discarding terminal output.
func readEnd(t *testing.T, ws *websocket.Conn) endMessage {
	t.Helper()
	for {
		kind, text := readBrowser(t, ws)
		if kind == websocket.MessageText {
			var end endMessage
			if err := json.Unmarshal([]byte(text), &end); err != nil {
				t.Fatalf("end message %q: %v", text, err)
			}
			return end
		}
	}
}

func writeBrowser(t *testing.T, ws *websocket.Conn, kind websocket.MessageType, data []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ws.Write(ctx, kind, data); err != nil {
		t.Fatalf("browser write: %v", err)
	}
}

func lastEvent(f *fixture, action string) (audit.Event, bool) {
	events := f.events()
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Action == action {
			return events[i], true
		}
	}
	return audit.Event{}, false
}

func waitEvent(t *testing.T, f *fixture, action string) audit.Event {
	t.Helper()
	for i := 0; i < 250; i++ {
		if event, ok := lastEvent(f, action); ok {
			return event
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no %s audit entry; have %+v", action, f.events())
	return audit.Event{}
}

func TestTerminalsNeedTheExecPermissionAndAreValidatedBeforeUpgrading(t *testing.T) {
	f := newFixture(t)
	f.connectAgent(agentExec)

	if _, resp, err := f.browser(execPath(agentExec, "web", ""), "", nil); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous = %v %v", resp, err)
	}
	if _, resp, err := f.browser(execPath(agentExec, "web", ""), auth.RoleViewer, nil); err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer = %v %v", resp, err)
	}
	denied := f.events()
	if len(denied) != 1 || denied[0].Action != "container.exec.open" || denied[0].Outcome != audit.OutcomeDenied || denied[0].ActorName != auth.RoleViewer {
		t.Fatalf("a refused terminal must be audited: %+v", denied)
	}
	if _, resp, err := f.browser(execPath(agentExec, "web", ""), auth.RoleOperator, http.Header{"Origin": []string{"https://evil.example"}}); err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin = %v %v", resp, err)
	}
	for _, bad := range []string{"?shell=zsh", "?shell=/bin/sh%20-c%20id", "?user=root%3Bid", "?user=%24%28id%29"} {
		if _, resp, err := f.browser(execPath(agentExec, "web", bad), auth.RoleOperator, nil); err == nil || resp == nil || resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s = %v %v, want 400", bad, resp, err)
		}
	}
	for _, bad := range []string{execPath("not-a-uuid", "web", ""), execPath(agentExec, "-bad", ""), execPath(agentExec, "a%20b", "")} {
		if _, resp, err := f.browser(bad, auth.RoleOperator, nil); err == nil || resp == nil || resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s = %v %v, want 404", bad, resp, err)
		}
	}
}

func TestTerminalsRelayBothWaysAndAreAudited(t *testing.T) {
	f := newFixture(t)
	agent := f.connectAgent(agentExec)
	ws, _, err := f.browser(execPath(agentExec, "web", "?shell=bash&user=1000&cols=120&rows=30"), auth.RoleOperator, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	open := agent.recv()
	var params protocol.DockerExecParams
	if open.Type != protocol.FrameStreamOpen || open.Method != protocol.MethodDockerExec || json.Unmarshal(open.Params, &params) != nil ||
		params.ID != "web" || params.Shell != "bash" || params.User != "1000" || params.Cols != 120 || params.Rows != 30 {
		t.Fatalf("agent saw %+v %+v", open, params)
	}

	// Output reaches the browser as binary, whatever bytes it holds.
	prompt := "\x1b[32mroot@web\x1b[0m# "
	agent.send(protocol.ChannelFrame{Type: protocol.FrameStreamData, ID: open.ID, Seq: 1, Data: []byte(prompt)})
	if kind, data := readBrowser(t, ws); kind != websocket.MessageBinary || data != prompt {
		t.Fatalf("output = %v %q", kind, data)
	}
	// Keystrokes reach the agent in order, and a resize is clamped to the supported range.
	writeBrowser(t, ws, websocket.MessageBinary, []byte("ls"))
	writeBrowser(t, ws, websocket.MessageBinary, []byte("\r"))
	if a, b := agent.recv(), agent.recv(); a.Type != protocol.FrameStreamInput || string(a.Data) != "ls" || string(b.Data) != "\r" {
		t.Fatalf("input frames = %+v %+v", a, b)
	}
	writeBrowser(t, ws, websocket.MessageText, []byte(`{"type":"resize","cols":9999,"rows":2}`))
	if frame := agent.recv(); frame.Type != protocol.FrameStreamControl || !strings.Contains(string(frame.Params), `"cols":500`) || !strings.Contains(string(frame.Params), `"rows":10`) {
		t.Fatalf("control frame = %+v", frame)
	}
	writeBrowser(t, ws, websocket.MessageText, []byte(`{"type":"reboot"}`))
	writeBrowser(t, ws, websocket.MessageText, []byte(`"junk"`))
	// A large paste is delivered in bounded pieces, in order, with nothing lost.
	paste := strings.Repeat("p", protocol.MaxExecInput*2+100)
	writeBrowser(t, ws, websocket.MessageBinary, []byte(paste))
	got := ""
	for pieces := 0; len(got) < len(paste); pieces++ {
		frame := agent.recv()
		if frame.Type != protocol.FrameStreamInput || len(frame.Data) > protocol.MaxExecInput {
			t.Fatalf("paste piece %d = type %s len %d", pieces, frame.Type, len(frame.Data))
		}
		got += string(frame.Data)
	}
	if got != paste {
		t.Fatal("the pasted text was altered")
	}

	// The shell exits with a status: the browser is told, and the session is audited.
	agent.send(protocol.ChannelFrame{Type: protocol.FrameStreamEnd, ID: open.ID, Error: &protocol.ChannelError{Code: protocol.ErrCodeExecExit, Message: "The shell exited with status 1."}})
	end := readEnd(t, ws)
	if !end.End || end.Code != protocol.ErrCodeExecExit || !strings.Contains(end.Message, "status 1") {
		t.Fatalf("end message = %+v", end)
	}
	opened := waitEvent(t, f, "container.exec.open")
	closed := waitEvent(t, f, "container.exec.close")
	if opened.Outcome != audit.OutcomeSuccess || opened.Detail["shell"] != "bash" || opened.Detail["user"] != "1000" {
		t.Fatalf("open audit = %+v", opened)
	}
	if closed.Detail["reason"] != reasonShellFailed || closed.Detail["bytesIn"] != int64(len("ls")+1+len(paste)) || closed.Detail["bytesOut"] != int64(len(prompt)) {
		t.Fatalf("close audit = %+v", closed.Detail)
	}
	for _, event := range f.events() {
		for key, value := range event.Detail {
			if text, ok := value.(string); ok && (strings.Contains(text, "root@web") || strings.Contains(text, "ppp")) {
				t.Errorf("terminal content leaked into the audit log: %s=%q", key, text)
			}
		}
	}
}

func TestTerminalsCloseWhenIdleOrTooOldOrWhenTheBrowserLeaves(t *testing.T) {
	setExecLimits(t, 300*time.Millisecond, 2*time.Second)
	f := newFixture(t)
	agent := f.connectAgent(agentExec)

	// Idle: output alone does not keep a terminal open, so it is closed and the agent told to stop.
	ws, _, err := f.browser(execPath(agentExec, "web", ""), auth.RoleOperator, nil)
	if err != nil {
		t.Fatal(err)
	}
	open := agent.recv()
	agent.send(protocol.ChannelFrame{Type: protocol.FrameStreamData, ID: open.ID, Seq: 1, Data: []byte("output keeps coming but nobody types")})
	if end := readEnd(t, ws); end.Code != reasonIdle || !strings.Contains(end.Message, "without input") {
		t.Fatalf("idle end = %+v", end)
	}
	if frame := agent.recv(); frame.Type != protocol.FrameCancel || frame.ID != open.ID {
		t.Fatalf("expected cancel, got %+v", frame)
	}
	if closed := waitEvent(t, f, "container.exec.close"); closed.Detail["reason"] != reasonIdle {
		t.Fatalf("idle audit = %+v", closed.Detail)
	}
	ws.CloseNow()

	// Typing keeps a terminal alive, but nothing outlives the maximum duration.
	ws, _, err = f.browser(execPath(agentExec, "web", ""), auth.RoleOperator, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	agent.recv()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(100 * time.Millisecond):
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				_ = ws.Write(ctx, websocket.MessageBinary, []byte(" "))
				cancel()
			}
		}
	}()
	if end := readEnd(t, ws); end.Code != reasonMaxDuration {
		t.Fatalf("max duration end = %+v", end)
	}
}

func TestLeavingTheTerminalPageCancelsTheAgentsShell(t *testing.T) {
	f := newFixture(t)
	agent := f.connectAgent(agentExec)
	ws, _, err := f.browser(execPath(agentExec, "web", ""), auth.RoleOperator, nil)
	if err != nil {
		t.Fatal(err)
	}
	open := agent.recv()
	ws.CloseNow()
	for {
		frame := agent.recv()
		if frame.Type == protocol.FrameCancel && frame.ID == open.ID {
			break
		}
	}
	if closed := waitEvent(t, f, "container.exec.close"); closed.Detail["reason"] != reasonClientLeft {
		t.Fatalf("close audit = %+v", closed.Detail)
	}
}

func TestTerminalsAreRefusedWithAClearReasonWhenTheHostCannotOpenThem(t *testing.T) {
	f := newFixture(t)
	f.connectAgent(agentManage) // manage tier: no terminals
	for agent, want := range map[string]string{agentManage: "Terminals are not enabled", agentOffline: "not connected"} {
		ws, _, err := f.browser(execPath(agent, "web", ""), auth.RoleAdmin, nil)
		if err != nil {
			t.Fatal(err)
		}
		if end := readEnd(t, ws); !end.End || !strings.Contains(end.Message, want) {
			t.Fatalf("%s: end = %+v, want mention of %q", agent, end, want)
		}
		ws.CloseNow()
	}
	if event, ok := lastEvent(f, "container.exec.open"); !ok || event.Outcome != audit.OutcomeFailure || event.Detail["reason"] != reasonStartFailed {
		t.Fatalf("failed open audit = %+v", event)
	}
}

func TestOnePersonCannotHoldUnlimitedTerminals(t *testing.T) {
	f := newFixture(t)
	agent := f.connectAgent(agentExec)
	var open []*websocket.Conn
	defer func() {
		for _, ws := range open {
			ws.CloseNow()
		}
	}()
	for i := 0; i < maxTerminalsPerUser; i++ {
		ws, _, err := f.browser(execPath(agentExec, "web", ""), auth.RoleOperator, nil)
		if err != nil {
			t.Fatal(err)
		}
		open = append(open, ws)
		agent.recv() // each terminal reached the agent, so its slot is certainly claimed
	}
	extra, _, err := f.browser(execPath(agentExec, "web", ""), auth.RoleOperator, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer extra.CloseNow()
	if end := readEnd(t, extra); end.Code != reasonTooManyOpen {
		t.Fatalf("extra terminal end = %+v", end)
	}
	// A different person is not affected by this person's terminals.
	other, _, err := f.browser(execPath(agentExec, "web", ""), auth.RoleAdmin, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer other.CloseNow()
	if frame := agent.recv(); frame.Type != protocol.FrameStreamOpen {
		t.Fatalf("another user's terminal was blocked: %+v", frame)
	}
}
