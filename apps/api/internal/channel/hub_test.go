package channel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/deranjer/vigara/internal/protocol"
)

const (
	agentA = "11111111-1111-4111-8111-111111111111"
	agentB = "22222222-2222-4222-8222-222222222222"
)

var testMethods = map[string]string{
	protocol.MethodPing: "",
	"test.manage":       protocol.DockerModeManage,
	"test.exec":         protocol.DockerModeExec,
	"test.stream":       "",
}

func testHub(t *testing.T, capabilities map[string][]string, mutate ...func(*Options)) (*Hub, *httptest.Server) {
	t.Helper()
	opts := Options{
		AuthenticateAgent: func(r *http.Request) (string, error) {
			id := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if _, ok := capabilities[id]; !ok {
				return "", errors.New("unknown")
			}
			return id, nil
		},
		Capabilities: func(_ context.Context, id string) ([]string, error) { return capabilities[id], nil },
		MethodTier:   func(m string) (string, bool) { tier, ok := testMethods[m]; return tier, ok },
		PingInterval: time.Hour,
	}
	for _, m := range mutate {
		m(&opts)
	}
	hub := NewHub(opts)
	server := httptest.NewServer(http.HandlerFunc(hub.ServeAgent))
	t.Cleanup(server.Close)
	return hub, server
}

type fakeAgent struct {
	t  *testing.T
	ws *websocket.Conn
}

func dialAgent(t *testing.T, server *httptest.Server, id string, version string) *fakeAgent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer " + id}}})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { ws.CloseNow() })
	a := &fakeAgent{t: t, ws: ws}
	a.send(protocol.ChannelFrame{Type: protocol.FrameHello, Version: version, AgentVersion: "test-1"})
	if version == protocol.ChannelVersion {
		if welcome := a.read(); welcome.Type != protocol.FrameWelcome {
			t.Fatalf("expected welcome, got %+v", welcome)
		}
	}
	return a
}

func (a *fakeAgent) send(frame protocol.ChannelFrame) {
	a.t.Helper()
	data, _ := json.Marshal(frame)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.ws.Write(ctx, websocket.MessageText, data); err != nil {
		a.t.Fatalf("agent write: %v", err)
	}
}

func (a *fakeAgent) read() protocol.ChannelFrame {
	a.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := a.ws.Read(ctx)
	if err != nil {
		a.t.Fatalf("agent read: %v", err)
	}
	var frame protocol.ChannelFrame
	if err := json.Unmarshal(data, &frame); err != nil {
		a.t.Fatal(err)
	}
	return frame
}

func waitOnline(t *testing.T, hub *Hub, id string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if _, ok := hub.Agent(id); ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("agent %s never registered", id)
}

func callCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

func TestHandshakeCallAndAgentList(t *testing.T) {
	hub, server := testHub(t, map[string][]string{agentA: {"docker.manage"}})
	if resp, err := http.Get(server.URL); err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request = %v, %v", resp, err)
	}
	agent := dialAgent(t, server, agentA, protocol.ChannelVersion)
	waitOnline(t, hub, agentA)
	info, _ := hub.Agent(agentA)
	if info.DockerTier != protocol.DockerModeManage || info.AgentVersion != "test-1" || len(hub.Agents()) != 1 {
		t.Fatalf("info = %+v", info)
	}

	go func() {
		call := agent.read()
		if call.Type != protocol.FrameCall || call.Method != protocol.MethodPing || call.DeadlineMillis <= 0 {
			t.Errorf("call frame = %+v", call)
		}
		agent.send(protocol.ChannelFrame{Type: protocol.FrameResult, ID: call.ID, Result: json.RawMessage(`{"pong":true,"agentVersion":"test-1"}`)})
	}()
	ctx, cancel := callCtx()
	defer cancel()
	var result protocol.ChannelPingResult
	if err := hub.Call(ctx, agentA, protocol.MethodPing, nil, &result); err != nil || !result.Pong || result.AgentVersion != "test-1" {
		t.Fatalf("Call = %+v, %v", result, err)
	}
}

func TestTierAndMethodChecksHappenBeforeAnythingIsSent(t *testing.T) {
	hub, server := testHub(t, map[string][]string{agentA: {}, agentB: {"docker.manage"}})
	dialAgent(t, server, agentA, protocol.ChannelVersion)
	manager := dialAgent(t, server, agentB, protocol.ChannelVersion)
	waitOnline(t, hub, agentA)
	waitOnline(t, hub, agentB)
	ctx, cancel := callCtx()
	defer cancel()

	var channelErr *protocol.ChannelError
	if err := hub.Call(ctx, agentA, "test.manage", nil, nil); !errors.As(err, &channelErr) || channelErr.Code != protocol.ErrCodeTierInsufficient {
		t.Fatalf("read-tier agent running a manage method: %v", err)
	}
	if err := hub.Call(ctx, agentB, "test.exec", nil, nil); !errors.As(err, &channelErr) || channelErr.Code != protocol.ErrCodeTierInsufficient {
		t.Fatalf("manage-tier agent running an exec method: %v", err)
	}
	if err := hub.Call(ctx, agentB, "test.unlisted", nil, nil); !errors.As(err, &channelErr) || channelErr.Code != protocol.ErrCodeUnknownMethod {
		t.Fatalf("unlisted method: %v", err)
	}
	if _, err := hub.OpenStream(ctx, agentA, "test.exec", nil); !errors.As(err, &channelErr) || channelErr.Code != protocol.ErrCodeTierInsufficient {
		t.Fatalf("stream tier check: %v", err)
	}
	if err := hub.Call(ctx, "99999999-9999-4999-8999-999999999999", protocol.MethodPing, nil, nil); !errors.Is(err, ErrAgentOffline) {
		t.Fatalf("offline agent: %v", err)
	}

	// Refused requests never reached the manager's socket; an allowed one does.
	go func() {
		call := manager.read()
		if call.Method != "test.manage" {
			t.Errorf("first frame the agent saw was %q, want test.manage", call.Method)
		}
		manager.send(protocol.ChannelFrame{Type: protocol.FrameResult, ID: call.ID, Error: &protocol.ChannelError{Code: protocol.ErrCodeBadRequest, Message: "no"}})
	}()
	if err := hub.Call(ctx, agentB, "test.manage", nil, nil); !errors.As(err, &channelErr) || channelErr.Code != protocol.ErrCodeBadRequest {
		t.Fatalf("agent error should pass through: %v", err)
	}
}

func TestStreamDeliversChunksInOrderAndEnds(t *testing.T) {
	hub, server := testHub(t, map[string][]string{agentA: {}})
	agent := dialAgent(t, server, agentA, protocol.ChannelVersion)
	waitOnline(t, hub, agentA)
	ctx, cancel := callCtx()
	defer cancel()
	stream, err := hub.OpenStream(ctx, agentA, "test.stream", map[string]int{"n": 3})
	if err != nil {
		t.Fatal(err)
	}
	open := agent.read()
	if open.Type != protocol.FrameStreamOpen || !strings.Contains(string(open.Params), `"n":3`) {
		t.Fatalf("open = %+v", open)
	}
	for i, chunk := range []string{"one", "two", "three"} {
		agent.send(protocol.ChannelFrame{Type: protocol.FrameStreamData, ID: open.ID, Seq: uint64(i + 1), Data: []byte(chunk)})
	}
	agent.send(protocol.ChannelFrame{Type: protocol.FrameStreamEnd, ID: open.ID})
	var got []string
	for chunk := range stream.C {
		got = append(got, string(chunk))
	}
	if strings.Join(got, ",") != "one,two,three" || stream.Err() != nil {
		t.Fatalf("got %v, err %v", got, stream.Err())
	}

	// A failed stream reports the agent's error after its data.
	failing, err := hub.OpenStream(ctx, agentA, "test.stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	open = agent.read()
	agent.send(protocol.ChannelFrame{Type: protocol.FrameStreamEnd, ID: open.ID, Error: &protocol.ChannelError{Code: protocol.ErrCodeInternal, Message: "boom"}})
	for range failing.C {
	}
	var channelErr *protocol.ChannelError
	if !errors.As(failing.Err(), &channelErr) || channelErr.Code != protocol.ErrCodeInternal {
		t.Fatalf("stream error = %v", failing.Err())
	}
}

func TestStreamCloseAndSlowConsumerCancelTheAgent(t *testing.T) {
	hub, server := testHub(t, map[string][]string{agentA: {}})
	agent := dialAgent(t, server, agentA, protocol.ChannelVersion)
	waitOnline(t, hub, agentA)
	ctx, cancel := callCtx()
	defer cancel()

	closed, err := hub.OpenStream(ctx, agentA, "test.stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	open := agent.read()
	closed.Close()
	if cancelFrame := agent.read(); cancelFrame.Type != protocol.FrameCancel || cancelFrame.ID != open.ID {
		t.Fatalf("expected cancel for %s, got %+v", open.ID, cancelFrame)
	}
	for range closed.C {
	}
	var channelErr *protocol.ChannelError
	if !errors.As(closed.Err(), &channelErr) || channelErr.Code != protocol.ErrCodeCancelled {
		t.Fatalf("closed stream error = %v", closed.Err())
	}

	slow, err := hub.OpenStream(ctx, agentA, "test.stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	open = agent.read()
	for i := 0; i < streamBuffer+50; i++ {
		agent.send(protocol.ChannelFrame{Type: protocol.FrameStreamData, ID: open.ID, Seq: uint64(i + 1), Data: []byte("x")})
	}
	deadline := time.After(5 * time.Second)
	for sawCancel := false; !sawCancel; {
		select {
		case <-deadline:
			t.Fatal("agent was never told to stop a slow consumer's stream")
		default:
		}
		if frame := agent.read(); frame.Type == protocol.FrameCancel && frame.ID == open.ID {
			sawCancel = true
		}
	}
	count := 0
	for range slow.C {
		count++
	}
	if !errors.As(slow.Err(), &channelErr) || channelErr.Code != protocol.ErrCodeSlowConsumer || count != streamBuffer {
		t.Fatalf("slow stream: %d chunks, err %v", count, slow.Err())
	}
}

func TestCancellationDisconnectionAndReplacement(t *testing.T) {
	hub, server := testHub(t, map[string][]string{agentA: {}})
	first := dialAgent(t, server, agentA, protocol.ChannelVersion)
	waitOnline(t, hub, agentA)

	// A deadline abandons the call and sends a cancel.
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- hub.Call(ctx, agentA, protocol.MethodPing, nil, nil) }()
	call := first.read()
	if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timed-out call = %v", err)
	}
	if frame := first.read(); frame.Type != protocol.FrameCancel || frame.ID != call.ID {
		t.Fatalf("expected cancel for %s, got %+v", call.ID, frame)
	}

	// Dropping the connection fails the call in flight.
	bg, stop := callCtx()
	defer stop()
	inflight := make(chan error, 1)
	go func() { inflight <- hub.Call(bg, agentA, protocol.MethodPing, nil, nil) }()
	first.read()
	first.ws.CloseNow()
	if err := <-inflight; !errors.Is(err, ErrAgentDisconnected) {
		t.Fatalf("call during disconnect = %v", err)
	}
	for i := 0; i < 100 && len(hub.Agents()) > 0; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if len(hub.Agents()) != 0 {
		t.Fatal("disconnected agent is still listed")
	}

	// A newer connection replaces an older one for the same agent.
	older := dialAgent(t, server, agentA, protocol.ChannelVersion)
	waitOnline(t, hub, agentA)
	newer := dialAgent(t, server, agentA, protocol.ChannelVersion)
	readCtx, readCancel := callCtx()
	defer readCancel()
	if _, _, err := older.ws.Read(readCtx); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("replaced connection ended with %v", err)
	}
	go func() {
		call := newer.read()
		newer.send(protocol.ChannelFrame{Type: protocol.FrameResult, ID: call.ID, Result: json.RawMessage(`{"pong":true}`)})
	}()
	if err := hub.Call(bg, agentA, protocol.MethodPing, nil, nil); err != nil {
		t.Fatalf("call after replacement: %v", err)
	}
}

func TestVersionMismatchAndInFlightLimit(t *testing.T) {
	hub, server := testHub(t, map[string][]string{agentA: {}}, func(o *Options) { o.MaxInFlight = 2 })
	bad := dialAgent(t, server, agentA, "v0")
	readCtx, cancel := callCtx()
	defer cancel()
	if _, _, err := bad.ws.Read(readCtx); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("version mismatch ended with %v", err)
	}
	if len(hub.Agents()) != 0 {
		t.Fatal("agent with an unsupported version was registered")
	}

	agent := dialAgent(t, server, agentA, protocol.ChannelVersion)
	waitOnline(t, hub, agentA)
	bg, stop := context.WithCancel(context.Background())
	defer stop()
	for i := 0; i < 2; i++ {
		go func() { _ = hub.Call(bg, agentA, protocol.MethodPing, nil, nil) }()
		agent.read()
	}
	var channelErr *protocol.ChannelError
	if err := hub.Call(bg, agentA, protocol.MethodPing, nil, nil); !errors.As(err, &channelErr) || channelErr.Code != protocol.ErrCodeBusy {
		t.Fatalf("third concurrent request = %v", err)
	}
}

func TestTierIsRefreshedWhenTheStoredCapabilitiesChange(t *testing.T) {
	// The channel can open before an agent's next poll reports a newly enabled tier, so
	// the tier cached at connect time must not stay authoritative.
	var stored atomic.Value
	stored.Store([]string{})
	hub, server := testHub(t, map[string][]string{agentA: nil, agentB: nil}, func(o *Options) {
		o.Capabilities = func(context.Context, string) ([]string, error) { return stored.Load().([]string), nil }
		o.PingInterval = 40 * time.Millisecond
	})
	manager := dialAgent(t, server, agentA, protocol.ChannelVersion)
	dialAgent(t, server, agentB, protocol.ChannelVersion)
	waitOnline(t, hub, agentA)
	waitOnline(t, hub, agentB)
	ctx, cancel := callCtx()
	defer cancel()

	var channelErr *protocol.ChannelError
	if err := hub.Call(ctx, agentA, "test.manage", nil, nil); !errors.As(err, &channelErr) || channelErr.Code != protocol.ErrCodeTierInsufficient {
		t.Fatalf("before the tier is enabled: %v", err)
	}
	if info, _ := hub.Agent(agentA); info.DockerTier != protocol.DockerModeRead {
		t.Fatalf("tier = %q", info.DockerTier)
	}

	stored.Store([]string{"docker.manage"})
	// A request that needs more than the cached tier is judged against the current one.
	go func() {
		call := manager.read()
		manager.send(protocol.ChannelFrame{Type: protocol.FrameResult, ID: call.ID, Result: json.RawMessage(`{}`)})
	}()
	if err := hub.Call(ctx, agentA, "test.manage", nil, nil); err != nil {
		t.Fatalf("after the tier is enabled: %v", err)
	}
	// And the periodic refresh updates what the fleet view reports, even with no requests.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if info, _ := hub.Agent(agentB); info.DockerTier == protocol.DockerModeManage {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	info, _ := hub.Agent(agentB)
	t.Fatalf("the reported tier never caught up: %q", info.DockerTier)
}

func TestStreamsCarryInputAndControlToTheAgent(t *testing.T) {
	hub, server := testHub(t, map[string][]string{agentA: {}})
	agent := dialAgent(t, server, agentA, protocol.ChannelVersion)
	waitOnline(t, hub, agentA)
	ctx, cancel := callCtx()
	defer cancel()
	stream, err := hub.OpenStream(ctx, agentA, "test.stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	open := agent.read()

	if err := stream.Send(ctx, []byte("ls\r")); err != nil {
		t.Fatal(err)
	}
	if frame := agent.read(); frame.Type != protocol.FrameStreamInput || frame.ID != open.ID || string(frame.Data) != "ls\r" {
		t.Fatalf("input frame = %+v", frame)
	}
	if err := stream.Control(ctx, protocol.ExecControl{Type: protocol.ControlResize, Cols: 120, Rows: 40}); err != nil {
		t.Fatal(err)
	}
	if frame := agent.read(); frame.Type != protocol.FrameStreamControl || frame.ID != open.ID || !strings.Contains(string(frame.Params), `"cols":120`) {
		t.Fatalf("control frame = %+v", frame)
	}
	var channelErr *protocol.ChannelError
	if err := stream.Send(ctx, make([]byte, protocol.MaxExecInput+1)); !errors.As(err, &channelErr) || channelErr.Code != protocol.ErrCodeBadRequest {
		t.Fatalf("oversized input = %v", err)
	}

	agent.send(protocol.ChannelFrame{Type: protocol.FrameStreamEnd, ID: open.ID})
	for range stream.C {
	}
	if err := stream.Send(ctx, []byte("late")); !errors.Is(err, ErrStreamClosed) {
		t.Fatalf("send after the end = %v", err)
	}
	if err := stream.Control(ctx, protocol.ExecControl{Type: protocol.ControlResize}); !errors.Is(err, ErrStreamClosed) {
		t.Fatalf("control after the end = %v", err)
	}
}
