package dockerchannel

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/deranjer/vigara/apps/agent/internal/channel"
	"github.com/deranjer/vigara/apps/agent/internal/dockerapi"
	"github.com/deranjer/vigara/internal/protocol"
)

type peer struct {
	t  *testing.T
	ws *websocket.Conn
}

func (p *peer) send(frame protocol.ChannelFrame) {
	p.t.Helper()
	data, _ := json.Marshal(frame)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.ws.Write(ctx, websocket.MessageText, data); err != nil {
		p.t.Fatal(err)
	}
}

func (p *peer) recv() protocol.ChannelFrame {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := p.ws.Read(ctx)
	if err != nil {
		p.t.Fatal(err)
	}
	var frame protocol.ChannelFrame
	if err := json.Unmarshal(data, &frame); err != nil {
		p.t.Fatal(err)
	}
	return frame
}

// startAgent connects a real channel client, with the Docker methods registered
// against the given fake Engine, to a fake server and returns the server's end.
func startAgent(t *testing.T, engine http.Handler) *peer {
	t.Helper()
	docker := httptest.NewServer(engine)
	t.Cleanup(docker.Close)
	api, err := dockerapi.New(docker.URL, docker.Client())
	if err != nil {
		t.Fatal(err)
	}
	conns := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ws.SetReadLimit(protocol.MaxChannelFrameBytes)
		if _, _, err := ws.Read(r.Context()); err != nil {
			return
		}
		welcome, _ := json.Marshal(protocol.ChannelFrame{Type: protocol.FrameWelcome, Version: protocol.ChannelVersion})
		_ = ws.Write(r.Context(), websocket.MessageText, welcome)
		conns <- ws
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	client := channel.NewClient(server.URL, "credential", "1.0.0", true, func() []string { return nil }, slog.New(slog.NewTextHandler(testWriter{t}, nil)))
	Register(client, api)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { client.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case ws := <-conns:
		ws.SetReadLimit(protocol.MaxChannelFrameBytes)
		t.Cleanup(func() { ws.CloseNow() })
		return &peer{t: t, ws: ws}
	case <-time.After(5 * time.Second):
		t.Fatal("agent never connected")
		return nil
	}
}

const inspectBody = `{"Id":"abc","Name":"/web","Path":"/run","Config":{"Image":"nginx","Env":["TOKEN=s3cret"]},"State":{"Status":"running","Running":true}}`

func engineHandler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1.44/containers/json":
			_, _ = w.Write([]byte(`[{"Id":"abc","Names":["/web"],"Image":"nginx","State":"running","Status":"Up","Created":1,"Labels":{"com.docker.compose.project":"site"}}]`))
		case strings.HasSuffix(r.URL.Path, "/gone/json"), strings.HasSuffix(r.URL.Path, "/gone/logs"), strings.HasSuffix(r.URL.Path, "/gone/stats"):
			http.NotFound(w, r)
		case strings.HasSuffix(r.URL.Path, "/json"):
			_, _ = w.Write([]byte(inspectBody))
		case strings.HasSuffix(r.URL.Path, "/logs"):
			payload := "hello\nworld\n"
			header := []byte{1, 0, 0, 0, 0, 0, 0, byte(len(payload))} // stdout frame: this container has no TTY
			_, _ = w.Write(append(header, payload...))
		default:
			t.Errorf("unexpected Docker request %s", r.URL.RequestURI())
			http.NotFound(w, r)
		}
	})
}

func TestDockerMethodsOverTheChannel(t *testing.T) {
	p := startAgent(t, engineHandler(t))

	p.send(protocol.ChannelFrame{Type: protocol.FrameCall, ID: "1", Method: protocol.MethodDockerList})
	reply := p.recv()
	var list protocol.DockerListResult
	if reply.Error != nil || json.Unmarshal(reply.Result, &list) != nil || len(list.Containers) != 1 || list.Containers[0].Project != "site" {
		t.Fatalf("list reply = %+v", reply)
	}

	p.send(protocol.ChannelFrame{Type: protocol.FrameCall, ID: "2", Method: protocol.MethodDockerInspect, Params: json.RawMessage(`{"id":"web"}`)})
	reply = p.recv()
	if reply.Error != nil || strings.Contains(string(reply.Result), "s3cret") || !strings.Contains(string(reply.Result), `"TOKEN"`) {
		t.Fatalf("inspect must name but not reveal env: %+v %s", reply.Error, reply.Result)
	}
	p.send(protocol.ChannelFrame{Type: protocol.FrameCall, ID: "3", Method: protocol.MethodDockerInspect, Params: json.RawMessage(`{"id":"web","revealEnv":true}`)})
	if reply = p.recv(); !strings.Contains(string(reply.Result), "s3cret") {
		t.Fatalf("revealed inspect = %+v", reply)
	}

	p.send(protocol.ChannelFrame{Type: protocol.FrameStreamOpen, ID: "4", Method: protocol.MethodDockerLogs, Params: json.RawMessage(`{"id":"web"}`)})
	var logged strings.Builder
	for {
		frame := p.recv()
		if frame.Type == protocol.FrameStreamEnd {
			if frame.Error != nil {
				t.Fatalf("log stream ended with %v", frame.Error)
			}
			break
		}
		logged.Write(frame.Data)
	}
	if !strings.Contains(logged.String(), `"m":"hello"`) || !strings.Contains(logged.String(), `"m":"world"`) {
		t.Fatalf("logs = %q", logged.String())
	}
}

func TestDockerErrorsAreMappedWithoutLeakingDetails(t *testing.T) {
	p := startAgent(t, engineHandler(t))
	expect := func(frame protocol.ChannelFrame, code string) {
		t.Helper()
		p.send(frame)
		reply := p.recv()
		if reply.ID != frame.ID || reply.Error == nil || reply.Error.Code != code {
			t.Fatalf("%s %s reply = %+v, want %s", frame.Method, string(frame.Params), reply, code)
		}
	}
	expect(protocol.ChannelFrame{Type: protocol.FrameCall, ID: "1", Method: protocol.MethodDockerInspect, Params: json.RawMessage(`{"id":"gone"}`)}, protocol.ErrCodeNotFound)
	expect(protocol.ChannelFrame{Type: protocol.FrameCall, ID: "2", Method: protocol.MethodDockerInspect, Params: json.RawMessage(`{"id":"../etc"}`)}, protocol.ErrCodeBadRequest)
	expect(protocol.ChannelFrame{Type: protocol.FrameCall, ID: "3", Method: protocol.MethodDockerInspect, Params: json.RawMessage(`{"id":"web","admin":true}`)}, protocol.ErrCodeBadRequest)
	expect(protocol.ChannelFrame{Type: protocol.FrameCall, ID: "4", Method: protocol.MethodDockerInspect}, protocol.ErrCodeBadRequest)
	for _, method := range []string{protocol.MethodDockerLogs, protocol.MethodDockerStats} {
		p.send(protocol.ChannelFrame{Type: protocol.FrameStreamOpen, ID: "s-" + method, Method: method, Params: json.RawMessage(`{"id":"gone"}`)})
		end := p.recv()
		if end.Type != protocol.FrameStreamEnd || end.Error == nil || end.Error.Code != protocol.ErrCodeNotFound {
			t.Fatalf("%s on a missing container = %+v", method, end)
		}
	}
}

func TestDockerMethodsAreUnknownUntilRegistered(t *testing.T) {
	// A host that did not opt in never calls Register, so the methods do not exist.
	conns := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		_, _, _ = ws.Read(r.Context())
		welcome, _ := json.Marshal(protocol.ChannelFrame{Type: protocol.FrameWelcome, Version: protocol.ChannelVersion})
		_ = ws.Write(r.Context(), websocket.MessageText, welcome)
		conns <- ws
		<-r.Context().Done()
	}))
	defer server.Close()
	client := channel.NewClient(server.URL, "credential", "1.0.0", true, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	go client.Run(ctx)
	defer cancel()
	p := &peer{t: t, ws: <-conns}
	defer p.ws.CloseNow()
	p.send(protocol.ChannelFrame{Type: protocol.FrameCall, ID: "1", Method: protocol.MethodDockerList})
	if reply := p.recv(); reply.Error == nil || reply.Error.Code != protocol.ErrCodeUnknownMethod {
		t.Fatalf("docker.list without opt-in = %+v", reply)
	}
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) { w.t.Log(strings.TrimSpace(string(p))); return len(p), nil }
