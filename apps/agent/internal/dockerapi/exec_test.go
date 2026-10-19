package dockerapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deranjer/vigara/internal/protocol"
)

// fakeExecEngine is a Docker Engine that implements exec, including the connection
// upgrade, with a tiny shell that echoes input and exits on "exit".
type fakeExecEngine struct {
	t          *testing.T
	mu         sync.Mutex
	createBody map[string]any
	startBody  map[string]any
	upgrade    string
	connection string
	resizes    []string
	exitCode   int
	startFails bool
	// greeting is written in the same packet as the response headers.
	greeting string
	calls    []string
	// announce, when set, is the PID the shell announces before its first output. With
	// splitAnnounce the announcement arrives in two separate writes.
	announce      string
	splitAnnounce bool
	// creates records every exec created (the terminal, then any hang-up), and detached
	// records the bodies of detached starts.
	creates  []map[string]any
	detached []map[string]any
}

func newExecClient(t *testing.T) (*Client, *fakeExecEngine) {
	t.Helper()
	e := &fakeExecEngine{t: t, greeting: "$ "}
	server := httptest.NewServer(http.HandlerFunc(e.serve))
	t.Cleanup(server.Close)
	client, err := New(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client, e
}

func (e *fakeExecEngine) serve(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1.44")
	e.mu.Lock()
	e.calls = append(e.calls, r.Method+" "+path)
	e.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && path == "/containers/web/exec":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		e.mu.Lock()
		if e.createBody == nil {
			e.createBody = body
		}
		e.creates = append(e.creates, body)
		id := "exec1"
		if len(e.creates) > 1 {
			id = "exec2"
		}
		e.mu.Unlock()
		writeJSON(w, http.StatusCreated, map[string]string{"Id": id})
	case r.Method == http.MethodPost && path == "/exec/exec2/start":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		e.mu.Lock()
		e.detached = append(e.detached, body)
		e.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodPost && path == "/containers/stopped/exec":
		writeJSON(w, http.StatusConflict, map[string]string{"message": "Container abc is not running"})
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/exec") && strings.Contains(path, "gone"):
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "No such container: gone"})
	case r.Method == http.MethodPost && path == "/exec/exec1/start":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		e.mu.Lock()
		e.startBody, e.upgrade, e.connection = body, r.Header.Get("Upgrade"), r.Header.Get("Connection")
		startFails := e.startFails
		e.mu.Unlock()
		if startFails {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "OCI runtime exec failed: exec: \"/bin/sh\": no such file"})
			return
		}
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			e.t.Errorf("hijack: %v", err)
			return
		}
		defer conn.Close()
		e.mu.Lock()
		announcement, split := "", e.splitAnnounce
		if e.announce != "" {
			announcement = "\x1b]777;vigara-pid=" + e.announce + "\x07"
		}
		e.mu.Unlock()
		headers := "HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.raw-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n"
		if split && len(announcement) > 8 {
			_, _ = rw.WriteString(headers + announcement[:8])
			_ = rw.Flush()
			time.Sleep(80 * time.Millisecond)
			_, _ = rw.WriteString(announcement[8:] + e.greeting)
		} else {
			_, _ = rw.WriteString(headers + announcement + e.greeting)
		}
		_ = rw.Flush()
		reader := bufio.NewReader(conn)
		buf := make([]byte, 1024)
		for {
			n, err := reader.Read(buf)
			if n > 0 {
				input := string(buf[:n])
				if strings.HasPrefix(input, "exit") {
					_, _ = conn.Write([]byte("bye\r\n"))
					return
				}
				_, _ = conn.Write([]byte("got:" + input))
			}
			if err != nil {
				return
			}
		}
	case r.Method == http.MethodPost && path == "/exec/exec1/resize":
		e.mu.Lock()
		e.resizes = append(e.resizes, "w="+r.URL.Query().Get("w")+" h="+r.URL.Query().Get("h"))
		e.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodGet && path == "/exec/exec1/json":
		e.mu.Lock()
		code := e.exitCode
		e.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"Running": false, "ExitCode": code})
	default:
		e.t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		http.NotFound(w, r)
	}
}

func readSome(t *testing.T, s *ExecSession, want string) string {
	t.Helper()
	got := ""
	buf := make([]byte, 256)
	deadline := time.After(5 * time.Second)
	for !strings.Contains(got, want) {
		done := make(chan struct{})
		var n int
		var err error
		go func() { n, err = s.Read(buf); close(done) }()
		select {
		case <-done:
			got += string(buf[:n])
			if err != nil {
				return got
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %q, got %q", want, got)
		}
	}
	return got
}

func TestExecOpensAnUpgradedTerminalAndMovesBytesBothWays(t *testing.T) {
	client, e := newExecClient(t)
	session, err := client.StartExec(context.Background(), protocol.DockerExecParams{ID: "web", Cols: 132, Rows: 43, User: "1000:1000"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	// The container's exec was created with a terminal, the chosen user, and only a shell to run.
	if e.createBody["Tty"] != true || e.createBody["AttachStdin"] != true || e.createBody["User"] != "1000:1000" {
		t.Fatalf("create body = %v", e.createBody)
	}
	cmd := e.createBody["Cmd"].([]any)
	if len(cmd) != 3 || cmd[0] != "/bin/sh" || !strings.Contains(cmd[2].(string), "exec bash || exec sh") {
		t.Fatalf("auto shell command = %v", cmd)
	}
	if env := e.createBody["Env"].([]any); len(env) == 0 || !strings.HasPrefix(env[0].(string), "TERM=") {
		t.Fatalf("env = %v", e.createBody["Env"])
	}
	if e.upgrade != "tcp" || !strings.EqualFold(e.connection, "Upgrade") {
		t.Fatalf("start must request a connection upgrade, got Upgrade=%q Connection=%q", e.upgrade, e.connection)
	}
	size := e.startBody["ConsoleSize"].([]any)
	if e.startBody["Tty"] != true || e.startBody["Detach"] != false || size[0] != float64(43) || size[1] != float64(132) {
		t.Fatalf("start body = %v", e.startBody)
	}

	// Output sent in the same packet as the response headers must not be lost.
	if got := readSome(t, session, "$ "); !strings.HasPrefix(got, "$ ") {
		t.Fatalf("greeting = %q", got)
	}
	if _, err := session.Write([]byte("ls\r")); err != nil {
		t.Fatal(err)
	}
	if got := readSome(t, session, "got:ls\r"); !strings.Contains(got, "got:ls\r") {
		t.Fatalf("echo = %q", got)
	}

	if err := session.Resize(context.Background(), 200, 5); err != nil {
		t.Fatal(err)
	}
	if len(e.resizes) != 1 || e.resizes[0] != "w=200 h=10" {
		t.Fatalf("resizes = %v (sizes are clamped to the supported range)", e.resizes)
	}

	e.exitCode = 3
	if _, err := session.Write([]byte("exit\r")); err != nil {
		t.Fatal(err)
	}
	if got := readSome(t, session, "bye"); !strings.Contains(got, "bye") {
		t.Fatalf("farewell = %q", got)
	}
	if _, err := io.ReadAll(session); err != nil {
		t.Fatalf("the stream should end cleanly: %v", err)
	}
	if code, err := session.ExitCode(context.Background()); err != nil || code != 3 {
		t.Fatalf("exit code = %d, %v", code, err)
	}
}

func TestExecShellChoices(t *testing.T) {
	for shell, want := range map[string]string{"sh": "exec /bin/sh", "bash": "exec /bin/bash"} {
		client, e := newExecClient(t)
		session, err := client.StartExec(context.Background(), protocol.DockerExecParams{ID: "web", Shell: shell})
		if err != nil {
			t.Fatal(err)
		}
		session.Close()
		cmd := e.createBody["Cmd"].([]any)
		if len(cmd) != 3 || cmd[0] != "/bin/sh" || cmd[1] != "-c" || !strings.HasSuffix(cmd[2].(string), want) || !strings.Contains(cmd[2].(string), "vigara-pid=") {
			t.Errorf("shell %q ran %v, want a /bin/sh wrapper that announces its PID and ends with %q", shell, cmd, want)
		}
		if _, hasUser := e.createBody["User"]; hasUser {
			t.Error("an unset user must not be sent, so the container's own user applies")
		}
	}
}

func TestExecRefusesBadInputBeforeTouchingDocker(t *testing.T) {
	client, e := newExecClient(t)
	for _, p := range []protocol.DockerExecParams{
		{ID: "../etc"}, {ID: ""}, {ID: "a b"}, {ID: "web", Shell: "zsh"}, {ID: "web", Shell: "/bin/sh -c id"}, {ID: "web", User: "root;id"}, {ID: "web", User: "$(id)"},
	} {
		if _, err := client.StartExec(context.Background(), p); !errors.Is(err, ErrBadRequest) {
			t.Errorf("StartExec(%+v) = %v, want ErrBadRequest", p, err)
		}
	}
	if len(e.calls) != 0 {
		t.Fatalf("invalid requests reached Docker: %v", e.calls)
	}
}

func TestExecReportsDockersReasonWhenItCannotStart(t *testing.T) {
	client, e := newExecClient(t)
	var apiErr *APIError
	if _, err := client.StartExec(context.Background(), protocol.DockerExecParams{ID: "stopped"}); !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict || !strings.Contains(apiErr.Message, "not running") {
		t.Fatalf("not running: %v", err)
	}
	if _, err := client.StartExec(context.Background(), protocol.DockerExecParams{ID: "gone"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing container: %v", err)
	}
	e.startFails = true
	if _, err := client.StartExec(context.Background(), protocol.DockerExecParams{ID: "web"}); !errors.As(err, &apiErr) || apiErr.Status != http.StatusInternalServerError || !strings.Contains(apiErr.Message, "OCI runtime") {
		t.Fatalf("start refused: %v", err)
	}
}
