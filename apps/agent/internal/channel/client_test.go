package channel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/deranjer/vigara/internal/protocol"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// fakeServer accepts agent channels and hands each welcomed connection to conns.
type fakeServer struct {
	*httptest.Server
	conns   chan *websocket.Conn
	hellos  chan protocol.ChannelFrame
	headers chan http.Header
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{conns: make(chan *websocket.Conn, 8), hellos: make(chan protocol.ChannelFrame, 8), headers: make(chan http.Header, 8)}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != protocol.ChannelPath {
			http.NotFound(w, r)
			return
		}
		f.headers <- r.Header.Clone()
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ws.SetReadLimit(protocol.MaxChannelFrameBytes)
		_, data, err := ws.Read(r.Context())
		if err != nil {
			return
		}
		var hello protocol.ChannelFrame
		_ = json.Unmarshal(data, &hello)
		f.hellos <- hello
		welcome, _ := json.Marshal(protocol.ChannelFrame{Type: protocol.FrameWelcome, Version: protocol.ChannelVersion})
		_ = ws.Write(r.Context(), websocket.MessageText, welcome)
		f.conns <- ws
		<-r.Context().Done()
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeServer) next(t *testing.T) *websocket.Conn {
	t.Helper()
	select {
	case ws := <-f.conns:
		return ws
	case <-time.After(5 * time.Second):
		t.Fatal("agent never connected")
		return nil
	}
}

func send(t *testing.T, ws *websocket.Conn, frame protocol.ChannelFrame) {
	t.Helper()
	data, _ := json.Marshal(frame)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ws.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("server write: %v", err)
	}
}

func recv(t *testing.T, ws *websocket.Conn) protocol.ChannelFrame {
	t.Helper()
	ws.SetReadLimit(protocol.MaxChannelFrameBytes)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := ws.Read(ctx)
	if err != nil {
		t.Fatalf("server read: %v", err)
	}
	var frame protocol.ChannelFrame
	if err := json.Unmarshal(data, &frame); err != nil {
		t.Fatal(err)
	}
	return frame
}

var testTiers = map[string]string{
	protocol.MethodPing: "", protocol.MethodInfo: "", "test.manage": protocol.DockerModeManage,
	"test.fail": "", "test.panic": "", "test.stream": "", "test.noimpl": "",
}

func startClient(t *testing.T, serverURL string, caps []string, tune ...func(*Client)) (*Client, context.CancelFunc) {
	t.Helper()
	c := NewClient(serverURL, "secret-credential", "9.9.9", true, func() []string { return caps }, quiet)
	c.MethodTier = func(m string) (string, bool) { tier, ok := testTiers[m]; return tier, ok }
	c.MinBackoff, c.MaxBackoff = 10*time.Millisecond, 40*time.Millisecond
	c.Calls["test.manage"] = func(context.Context, json.RawMessage) (any, error) { return map[string]bool{"managed": true}, nil }
	c.Calls["test.fail"] = func(context.Context, json.RawMessage) (any, error) {
		return nil, errors.New("open /etc/shadow: permission denied")
	}
	c.Calls["test.panic"] = func(context.Context, json.RawMessage) (any, error) { panic("kaboom") }
	for _, f := range tune {
		f(c)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return c, cancel
}

func TestWebSocketURLAppliesTheServerURLPolicy(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		allow bool
		want  string
		ok    bool
	}{
		{"https://vigira.example", false, "wss://vigira.example/api/agent/channel", true},
		{"https://vigira.example/base/", false, "wss://vigira.example/base/api/agent/channel", true},
		{"http://localhost:8080", false, "ws://localhost:8080/api/agent/channel", true},
		{"http://192.168.1.97:8080", false, "", false},
		{"http://192.168.1.97:8080", true, "ws://192.168.1.97:8080/api/agent/channel", true},
		{"http://vigira.example", true, "", false},
	} {
		got, err := WebSocketURL(tc.raw, tc.allow)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("WebSocketURL(%q, %v) = %q, %v; want %q ok=%v", tc.raw, tc.allow, got, err, tc.want, tc.ok)
		}
	}
}

func TestHelloCredentialAndBuiltInMethods(t *testing.T) {
	server := newFakeServer(t)
	startClient(t, server.URL, []string{"docker.manage"})
	ws := server.next(t)
	defer ws.CloseNow()
	if header := <-server.headers; header.Get("Authorization") != "Bearer secret-credential" {
		t.Fatalf("credential header = %q", header.Get("Authorization"))
	}
	if hello := <-server.hellos; hello.Version != protocol.ChannelVersion || hello.AgentVersion != "9.9.9" || len(hello.Capabilities) != 1 {
		t.Fatalf("hello = %+v", hello)
	}

	send(t, ws, protocol.ChannelFrame{Type: protocol.FrameCall, ID: "1", Method: protocol.MethodPing})
	var ping protocol.ChannelPingResult
	reply := recv(t, ws)
	if reply.ID != "1" || reply.Error != nil || json.Unmarshal(reply.Result, &ping) != nil || !ping.Pong || ping.AgentVersion != "9.9.9" {
		t.Fatalf("ping reply = %+v", reply)
	}
	send(t, ws, protocol.ChannelFrame{Type: protocol.FrameCall, ID: "2", Method: protocol.MethodInfo})
	var info protocol.ChannelInfoResult
	reply = recv(t, ws)
	if json.Unmarshal(reply.Result, &info) != nil || info.DockerTier != protocol.DockerModeManage {
		t.Fatalf("info reply = %+v", reply)
	}
}

func TestAgentEnforcesItsOwnTierAndHidesInternalErrors(t *testing.T) {
	server := newFakeServer(t)
	startClient(t, server.URL, nil) // the local operator enabled no management tier
	ws := server.next(t)
	defer ws.CloseNow()

	expect := func(id, method, code string) protocol.ChannelFrame {
		t.Helper()
		send(t, ws, protocol.ChannelFrame{Type: protocol.FrameCall, ID: id, Method: method})
		reply := recv(t, ws)
		if reply.ID != id || reply.Error == nil || reply.Error.Code != code {
			t.Fatalf("%s reply = %+v, want error %s", method, reply, code)
		}
		return reply
	}
	expect("1", "test.manage", protocol.ErrCodeTierInsufficient)
	expect("2", "test.unknown", protocol.ErrCodeUnknownMethod)
	expect("3", "test.noimpl", protocol.ErrCodeUnknownMethod)
	if reply := expect("4", "test.fail", protocol.ErrCodeInternal); strings.Contains(reply.Error.Message, "shadow") {
		t.Fatalf("host details leaked to the server: %q", reply.Error.Message)
	}
	expect("5", "test.panic", protocol.ErrCodeInternal)
	// The agent survives a panic and keeps answering.
	send(t, ws, protocol.ChannelFrame{Type: protocol.FrameCall, ID: "6", Method: protocol.MethodPing})
	if reply := recv(t, ws); reply.Error != nil {
		t.Fatalf("ping after panic = %+v", reply)
	}
}

func TestStreamsChunkAndStopOnCancel(t *testing.T) {
	server := newFakeServer(t)
	var stopped atomic.Bool
	startClient(t, server.URL, nil, func(c *Client) {
		c.Streams["test.stream"] = func(ctx context.Context, params json.RawMessage, send func([]byte) error) error {
			if string(params) == `"big"` {
				return send([]byte(strings.Repeat("a", protocol.MaxStreamChunkBytes*2+10)))
			}
			if err := send([]byte("first")); err != nil {
				return err
			}
			<-ctx.Done()
			stopped.Store(true)
			return ctx.Err()
		}
	})
	ws := server.next(t)
	defer ws.CloseNow()

	send(t, ws, protocol.ChannelFrame{Type: protocol.FrameStreamOpen, ID: "big", Method: "test.stream", Params: json.RawMessage(`"big"`)})
	var total, chunks int
	var lastSeq uint64
	for {
		frame := recv(t, ws)
		if frame.Type == protocol.FrameStreamEnd {
			if frame.Error != nil {
				t.Fatalf("stream ended with %v", frame.Error)
			}
			break
		}
		if frame.Type != protocol.FrameStreamData || frame.Seq != lastSeq+1 || len(frame.Data) > protocol.MaxStreamChunkBytes {
			t.Fatalf("frame = type %s seq %d len %d", frame.Type, frame.Seq, len(frame.Data))
		}
		lastSeq, total, chunks = frame.Seq, total+len(frame.Data), chunks+1
	}
	if total != protocol.MaxStreamChunkBytes*2+10 || chunks != 3 {
		t.Fatalf("received %d bytes in %d chunks", total, chunks)
	}

	send(t, ws, protocol.ChannelFrame{Type: protocol.FrameStreamOpen, ID: "live", Method: "test.stream"})
	if first := recv(t, ws); string(first.Data) != "first" {
		t.Fatalf("first chunk = %+v", first)
	}
	send(t, ws, protocol.ChannelFrame{Type: protocol.FrameCancel, ID: "live"})
	for i := 0; i < 100 && !stopped.Load(); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if !stopped.Load() {
		t.Fatal("cancel did not stop the stream handler")
	}
	// Streams on a method that is not a stream are refused.
	send(t, ws, protocol.ChannelFrame{Type: protocol.FrameStreamOpen, ID: "wrong", Method: protocol.MethodPing})
	for {
		frame := recv(t, ws)
		if frame.ID != "wrong" {
			continue // an end frame for the cancelled stream may arrive first
		}
		if frame.Type != protocol.FrameStreamEnd || frame.Error == nil || frame.Error.Code != protocol.ErrCodeUnknownMethod {
			t.Fatalf("stream open on a call method = %+v", frame)
		}
		break
	}
}

func TestDeadlineIsEnforcedOnTheAgent(t *testing.T) {
	server := newFakeServer(t)
	startClient(t, server.URL, nil, func(c *Client) {
		c.Calls["test.fail"] = func(ctx context.Context, _ json.RawMessage) (any, error) { <-ctx.Done(); return nil, ctx.Err() }
	})
	ws := server.next(t)
	defer ws.CloseNow()
	send(t, ws, protocol.ChannelFrame{Type: protocol.FrameCall, ID: "slow", Method: "test.fail", DeadlineMillis: 50})
	if reply := recv(t, ws); reply.Error == nil || reply.Error.Code != protocol.ErrCodeTimeout {
		t.Fatalf("reply = %+v", reply)
	}
}

func TestReconnectsAfterTheServerDropsTheConnection(t *testing.T) {
	server := newFakeServer(t)
	startClient(t, server.URL, nil)
	first := server.next(t)
	first.Close(websocket.StatusGoingAway, "restart")
	second := server.next(t)
	defer second.CloseNow()
	send(t, second, protocol.ChannelFrame{Type: protocol.FrameCall, ID: "1", Method: protocol.MethodPing})
	if reply := recv(t, second); reply.Error != nil {
		t.Fatalf("ping after reconnect = %+v", reply)
	}
}

func TestServerWithoutChannelSupportIsReportedAsUnsupported(t *testing.T) {
	old := httptest.NewServer(http.NotFoundHandler())
	defer old.Close()
	c := NewClient(old.URL, "credential", "1", true, nil, quiet)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if unsupported, err := c.session(ctx); !unsupported || err != nil {
		t.Fatalf("session = unsupported %v, err %v", unsupported, err)
	}
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "x", http.StatusInternalServerError) }))
	defer broken.Close()
	c = NewClient(broken.URL, "credential", "1", true, nil, quiet)
	if unsupported, err := c.session(ctx); unsupported || err == nil {
		t.Fatalf("server error session = unsupported %v, err %v", unsupported, err)
	}
}

func TestRefusesPlainHTTPToAPublicHost(t *testing.T) {
	c := NewClient("http://vigira.example", "credential", "1", true, nil, quiet)
	if _, err := c.session(context.Background()); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("session error = %v", err)
	}
}
