package dockerchannel

import (
	"context"
	"encoding/json"
	"fmt"
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
func startAgent(t *testing.T, engine http.Handler, capabilities ...string) *peer {
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
	client := channel.NewClient(server.URL, "credential", "1.0.0", true, func() []string { return capabilities }, slog.New(slog.NewTextHandler(testWriter{t}, nil)))
	Register(client, api)
	RegisterManage(client, api) // registered everywhere here so the tier gate itself is what is tested
	RegisterExec(client, api)
	RegisterResources(client, api)
	RegisterResourceManagement(client, api)
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

const runningInspect = `{"Id":"abc","Name":"/web","Image":"sha256:old","State":{"Running":true},"Config":{"Image":"app:1"},"HostConfig":{"NetworkMode":"bridge"},"NetworkSettings":{"Networks":{}}}`

func manageEngine(t *testing.T) (http.Handler, *[]string) {
	var calls []string
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+strings.TrimPrefix(r.URL.Path, "/v1.44"))
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/web/json"):
			_, _ = w.Write([]byte(runningInspect))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/gone/json"):
			http.NotFound(w, r)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/web/restart"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/web"):
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"message":"You cannot remove a running container. Stop the container before attempting removal or force remove"}`))
		case r.URL.Path == "/v1.44/images/create":
			_, _ = w.Write([]byte(`{"error":"pull access denied for app, repository does not exist"}` + "\n"))
		default:
			t.Errorf("unexpected Docker request %s %s", r.Method, r.URL.RequestURI())
			http.NotFound(w, r)
		}
	}), &calls
}

func TestManageMethodsAreRefusedBelowTheManageTier(t *testing.T) {
	engine, calls := manageEngine(t)
	for _, caps := range [][]string{nil, {}} {
		p := startAgent(t, engine, caps...)
		p.send(protocol.ChannelFrame{Type: protocol.FrameCall, ID: "1", Method: protocol.MethodDockerAction, Params: json.RawMessage(`{"id":"web","action":"restart"}`)})
		if reply := p.recv(); reply.Error == nil || reply.Error.Code != protocol.ErrCodeTierInsufficient {
			t.Fatalf("restart on a read-only host = %+v", reply)
		}
		p.send(protocol.ChannelFrame{Type: protocol.FrameStreamOpen, ID: "2", Method: protocol.MethodDockerRecreate, Params: json.RawMessage(`{"id":"web"}`)})
		if end := p.recv(); end.Type != protocol.FrameStreamEnd || end.Error == nil || end.Error.Code != protocol.ErrCodeTierInsufficient {
			t.Fatalf("recreate on a read-only host = %+v", end)
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("a refused action still reached Docker: %v", *calls)
	}
}

func TestManageActionsRunAtTheManageTier(t *testing.T) {
	engine, calls := manageEngine(t)
	p := startAgent(t, engine, "docker.manage")
	send := func(id, params string) protocol.ChannelFrame {
		p.send(protocol.ChannelFrame{Type: protocol.FrameCall, ID: id, Method: protocol.MethodDockerAction, Params: json.RawMessage(params)})
		return p.recv()
	}
	var result protocol.DockerActionResult
	reply := send("1", `{"id":"web","action":"restart"}`)
	if reply.Error != nil || json.Unmarshal(reply.Result, &result) != nil || !result.Changed || result.Name != "web" || result.Action != "restart" {
		t.Fatalf("restart reply = %+v", reply)
	}
	if reply = send("2", `{"id":"web","action":"remove"}`); reply.Error == nil || reply.Error.Code != protocol.ErrCodeConflict || !strings.Contains(reply.Error.Message, "running container") {
		t.Fatalf("remove of a running container = %+v", reply)
	}
	if reply = send("3", `{"id":"gone","action":"restart"}`); reply.Error == nil || reply.Error.Code != protocol.ErrCodeNotFound {
		t.Fatalf("missing container = %+v", reply)
	}
	for _, bad := range []string{`{"id":"web","action":"exec"}`, `{"id":"../x","action":"restart"}`, `{"id":"web","action":"restart","extra":1}`, `{}`} {
		if reply = send("4", bad); reply.Error == nil || reply.Error.Code != protocol.ErrCodeBadRequest {
			t.Errorf("%s = %+v, want bad_request", bad, reply)
		}
	}
	for _, call := range *calls {
		if strings.HasPrefix(call, "POST /containers/../") {
			t.Errorf("a path escape reached Docker: %s", call)
		}
	}
}

func TestRecreateStreamsProgressAndReportsFailures(t *testing.T) {
	engine, _ := manageEngine(t)
	p := startAgent(t, engine, "docker.manage")
	p.send(protocol.ChannelFrame{Type: protocol.FrameStreamOpen, ID: "r", Method: protocol.MethodDockerRecreate, Params: json.RawMessage(`{"id":"web","pull":true}`)})
	var progress strings.Builder
	for {
		frame := p.recv()
		if frame.Type == protocol.FrameStreamData {
			progress.Write(frame.Data)
			continue
		}
		if frame.Type != protocol.FrameStreamEnd || frame.Error == nil || frame.Error.Code != protocol.ErrCodeRecreateFailed ||
			!strings.Contains(frame.Error.Message, "the original container was not changed") || !strings.Contains(frame.Error.Message, "pull access denied") {
			t.Fatalf("stream end = %+v", frame)
		}
		break
	}
	if !strings.Contains(progress.String(), `"step":"pull"`) || strings.Contains(progress.String(), `"step":"done"`) {
		t.Fatalf("progress = %q", progress.String())
	}
}

// execEngine is a Docker Engine with exec: a hijacked connection acting as a tiny shell.
type execEngine struct {
	t        *testing.T
	resizes  chan string
	exitCode int
	failExec bool
}

func (e *execEngine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1.44")
	switch {
	case r.Method == http.MethodPost && path == "/containers/web/exec":
		if e.failExec {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"OCI runtime exec failed: exec: \"/bin/sh\": no such file or directory"}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"Id":"exec1"}`))
	case r.Method == http.MethodPost && path == "/containers/stopped/exec":
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"message":"Container is not running"}`))
	case r.Method == http.MethodPost && path == "/exec/exec1/start":
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			e.t.Errorf("hijack: %v", err)
			return
		}
		defer conn.Close()
		_, _ = rw.WriteString("HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.raw-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n$ ")
		_ = rw.Flush()
		buf := make([]byte, 512)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				if strings.HasPrefix(string(buf[:n]), "exit") {
					_, _ = conn.Write([]byte("bye\r\n"))
					return
				}
				_, _ = conn.Write(append([]byte("got:"), buf[:n]...))
			}
			if err != nil {
				return
			}
		}
	case r.Method == http.MethodPost && path == "/exec/exec1/resize":
		e.resizes <- "w=" + r.URL.Query().Get("w") + " h=" + r.URL.Query().Get("h")
	case r.Method == http.MethodGet && path == "/exec/exec1/json":
		_, _ = w.Write([]byte(`{"Running":false,"ExitCode":` + strconvItoa(e.exitCode) + `}`))
	default:
		e.t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		http.NotFound(w, r)
	}
}

func strconvItoa(n int) string { return fmt.Sprintf("%d", n) }

func readOutput(t *testing.T, p *peer, want string) string {
	t.Helper()
	var out strings.Builder
	for !strings.Contains(out.String(), want) {
		frame := p.recv()
		if frame.Type == protocol.FrameStreamEnd {
			t.Fatalf("stream ended before %q: %+v (output %q)", want, frame.Error, out.String())
		}
		out.Write(frame.Data)
	}
	return out.String()
}

func TestTerminalRelaysKeystrokesResizesAndTheExitStatus(t *testing.T) {
	engine := &execEngine{t: t, resizes: make(chan string, 4), exitCode: 2}
	p := startAgent(t, engine, "docker.manage", "docker.exec")
	p.send(protocol.ChannelFrame{Type: protocol.FrameStreamOpen, ID: "t", Method: protocol.MethodDockerExec, Params: json.RawMessage(`{"id":"web","cols":100,"rows":30}`)})
	readOutput(t, p, "$ ")

	p.send(protocol.ChannelFrame{Type: protocol.FrameStreamInput, ID: "t", Data: []byte("ls\r")})
	readOutput(t, p, "got:ls\r")
	p.send(protocol.ChannelFrame{Type: protocol.FrameStreamControl, ID: "t", Params: json.RawMessage(`{"type":"resize","cols":150,"rows":40}`)})
	select {
	case got := <-engine.resizes:
		if got != "w=150 h=40" {
			t.Fatalf("resize = %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the resize never reached Docker")
	}
	// Unknown control messages are ignored rather than breaking the terminal.
	p.send(protocol.ChannelFrame{Type: protocol.FrameStreamControl, ID: "t", Params: json.RawMessage(`{"type":"reboot"}`)})
	p.send(protocol.ChannelFrame{Type: protocol.FrameStreamControl, ID: "t", Params: json.RawMessage(`"a string, not a control object"`)})

	p.send(protocol.ChannelFrame{Type: protocol.FrameStreamInput, ID: "t", Data: []byte("exit\r")})
	readOutput(t, p, "bye")
	end := p.recv()
	if end.Type != protocol.FrameStreamEnd || end.Error == nil || end.Error.Code != protocol.ErrCodeExecExit || !strings.Contains(end.Error.Message, "status 2") {
		t.Fatalf("end = %+v", end)
	}
}

func TestTerminalNeedsTheExecTierAndReportsWhyItCannotStart(t *testing.T) {
	engine := &execEngine{t: t, resizes: make(chan string, 4)}
	// A host at the manage tier must not open terminals, even though the handler is registered.
	p := startAgent(t, engine, "docker.manage")
	p.send(protocol.ChannelFrame{Type: protocol.FrameStreamOpen, ID: "t", Method: protocol.MethodDockerExec, Params: json.RawMessage(`{"id":"web"}`)})
	if end := p.recv(); end.Type != protocol.FrameStreamEnd || end.Error == nil || end.Error.Code != protocol.ErrCodeTierInsufficient {
		t.Fatalf("manage-tier terminal = %+v", end)
	}

	engine2 := &execEngine{t: t, resizes: make(chan string, 4)}
	q := startAgent(t, engine2, "docker.manage", "docker.exec")
	for id, tc := range map[string]struct {
		params string
		code   string
	}{
		"a": {`{"id":"stopped"}`, protocol.ErrCodeConflict},
		"b": {`{"id":"web","shell":"zsh"}`, protocol.ErrCodeBadRequest},
		"c": {`{"id":"web","user":"root;id"}`, protocol.ErrCodeBadRequest},
		"d": {`{"id":"web","extra":true}`, protocol.ErrCodeBadRequest},
	} {
		q.send(protocol.ChannelFrame{Type: protocol.FrameStreamOpen, ID: id, Method: protocol.MethodDockerExec, Params: json.RawMessage(tc.params)})
		if end := q.recv(); end.Type != protocol.FrameStreamEnd || end.Error == nil || end.Error.Code != tc.code {
			t.Errorf("%s = %+v, want %s", tc.params, end, tc.code)
		}
	}
	engine2.failExec = true
	q.send(protocol.ChannelFrame{Type: protocol.FrameStreamOpen, ID: "e", Method: protocol.MethodDockerExec, Params: json.RawMessage(`{"id":"web"}`)})
	if end := q.recv(); end.Error == nil || end.Error.Code != protocol.ErrCodeExecFailed || !strings.Contains(end.Error.Message, "no such file") {
		t.Fatalf("a missing shell must be explained: %+v", end)
	}
}

func resourceEngine(t *testing.T, calls *[]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls = append(*calls, r.Method+" "+strings.TrimPrefix(r.URL.Path, "/v1.44"))
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/containers/json":
			_, _ = w.Write([]byte(`[{"Id":"c1","Names":["/web"],"ImageID":"sha256:a","Mounts":[],"NetworkSettings":{"Networks":{}}}]`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/images/json":
			_, _ = w.Write([]byte(`[{"Id":"sha256:a","RepoTags":["app:1"],"Created":5,"Size":10},{"Id":"sha256:b","RepoTags":null,"Created":4,"Size":20}]`))
		case r.Method == http.MethodDelete && r.URL.Path == "/v1.44/networks/bridge":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"bridge is a pre-defined network and cannot be removed"}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/v1.44/volumes/gone":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"no such volume"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1.44/images/create":
			_, _ = w.Write([]byte(`{"error":"pull access denied for private/app"}` + "\n"))
		default:
			t.Errorf("unexpected Docker request %s %s", r.Method, r.URL.RequestURI())
			http.NotFound(w, r)
		}
	})
}

func TestResourceReadsWorkWithoutAnyTierAndManagementDoesNot(t *testing.T) {
	var calls []string
	p := startAgent(t, resourceEngine(t, &calls)) // a read-only host
	p.send(protocol.ChannelFrame{Type: protocol.FrameCall, ID: "1", Method: protocol.MethodDockerImages})
	reply := p.recv()
	var images protocol.DockerImagesResult
	if reply.Error != nil || json.Unmarshal(reply.Result, &images) != nil || len(images.Images) != 2 || len(images.Images[0].UsedBy) != 1 || !images.Images[1].Dangling {
		t.Fatalf("images reply = %+v", reply)
	}
	before := len(calls)
	for id, tc := range map[string]struct{ method, params string }{
		"2": {protocol.MethodDockerResourceRemove, `{"kind":"image","id":"app:1"}`},
		"3": {protocol.MethodDockerPrune, `{"kind":"volume","dryRun":true}`},
	} {
		p.send(protocol.ChannelFrame{Type: protocol.FrameCall, ID: id, Method: tc.method, Params: json.RawMessage(tc.params)})
		if reply := p.recv(); reply.Error == nil || reply.Error.Code != protocol.ErrCodeTierInsufficient {
			t.Fatalf("%s on a read-only host = %+v", tc.method, reply)
		}
	}
	p.send(protocol.ChannelFrame{Type: protocol.FrameStreamOpen, ID: "4", Method: protocol.MethodDockerPull, Params: json.RawMessage(`{"ref":"redis:7"}`)})
	if end := p.recv(); end.Type != protocol.FrameStreamEnd || end.Error == nil || end.Error.Code != protocol.ErrCodeTierInsufficient {
		t.Fatalf("pull on a read-only host = %+v", end)
	}
	if len(calls) != before {
		t.Fatalf("a refused method still reached Docker: %v", calls[before:])
	}
}

func TestResourceErrorsKeepDockersExplanationAndRejectBadInput(t *testing.T) {
	var calls []string
	p := startAgent(t, resourceEngine(t, &calls), "docker.manage")
	call := func(id, method, params string) protocol.ChannelFrame {
		p.send(protocol.ChannelFrame{Type: protocol.FrameCall, ID: id, Method: method, Params: json.RawMessage(params)})
		return p.recv()
	}
	if reply := call("1", protocol.MethodDockerResourceRemove, `{"kind":"network","id":"bridge"}`); reply.Error == nil || reply.Error.Code != protocol.ErrCodeConflict || !strings.Contains(reply.Error.Message, "pre-defined network") {
		t.Fatalf("a refused network removal = %+v", reply)
	}
	if reply := call("2", protocol.MethodDockerResourceRemove, `{"kind":"volume","id":"gone"}`); reply.Error == nil || reply.Error.Code != protocol.ErrCodeNotFound || strings.Contains(reply.Error.Message, "container") {
		t.Fatalf("a missing volume = %+v", reply)
	}
	for id, params := range map[string]string{"3": `{"kind":"image","id":"../x"}`, "4": `{"kind":"secret","id":"x"}`, "5": `{"kind":"volume","id":"a","extra":1}`, "6": `{}`} {
		if reply := call(id, protocol.MethodDockerResourceRemove, params); reply.Error == nil || reply.Error.Code != protocol.ErrCodeBadRequest {
			t.Errorf("%s = %+v, want bad_request", params, reply)
		}
	}
	if reply := call("7", protocol.MethodDockerPrune, `{"kind":"container"}`); reply.Error == nil || reply.Error.Code != protocol.ErrCodeBadRequest {
		t.Fatalf("pruning an unknown kind = %+v", reply)
	}
	p.send(protocol.ChannelFrame{Type: protocol.FrameStreamOpen, ID: "8", Method: protocol.MethodDockerPull, Params: json.RawMessage(`{"ref":"private/app:1"}`)})
	for {
		frame := p.recv()
		if frame.Type == protocol.FrameStreamEnd {
			if frame.Error == nil || frame.Error.Code != protocol.ErrCodePullFailed || !strings.Contains(frame.Error.Message, "pull access denied") {
				t.Fatalf("a failed pull must say why: %+v", frame.Error)
			}
			break
		}
	}
	p.send(protocol.ChannelFrame{Type: protocol.FrameStreamOpen, ID: "9", Method: protocol.MethodDockerPull, Params: json.RawMessage(`{"ref":"sha256:0123456789ab"}`)})
	if end := p.recv(); end.Error == nil || end.Error.Code != protocol.ErrCodeBadRequest {
		t.Fatalf("pulling by ID = %+v", end)
	}
	for _, call := range calls {
		if strings.Contains(call, "..") {
			t.Errorf("a path escape reached Docker: %s", call)
		}
	}
}
