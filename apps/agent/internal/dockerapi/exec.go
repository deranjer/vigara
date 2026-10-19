package dockerapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/deranjer/vigara/internal/protocol"
)

const (
	execSetupTimeout = 20 * time.Second
	execInfoTimeout  = 5 * time.Second
)

// ExecSession is an interactive terminal inside a container. Docker upgrades the
// exec-start request to a raw two-way byte stream, so output arrives through Read
// and keystrokes go out through Write.
type ExecSession struct {
	client      *Client
	execID      string
	containerID string
	user        string
	conn        net.Conn
	reader      *bufio.Reader

	// Docker does not stop an exec'd process when its client disconnects, so the shell
	// announces its own PID (see shellCommand) and Close signals it. pid stays zero when
	// the announcement was not seen, and ended is set once the shell has exited by itself.
	mu      sync.Mutex
	pid     int
	scanned bool
	pending []byte
	ended   bool
}

// pidAnnouncement is how the shell reports its process ID inside the container: a
// private terminal escape sequence that terminals ignore and Read strips.
const (
	pidPrefix = "\x1b]777;vigara-pid="
	pidSuffix = "\x07"
	// A shell prints its announcement first, so waiting for more than this many bytes
	// means there is none and the output is passed through untouched.
	pidScanLimit = 256
)

// announcePID prints the shell's process ID as a private escape sequence. The shell
// then replaces itself with the real one using exec, so the PID stays the same.
const announcePID = `printf '\033]777;vigara-pid=%s\007' "$$"; `

// shellCommand returns the command a terminal runs for the chosen shell. "auto"
// prefers bash and falls back to sh, because many images only ship sh. Every choice
// first announces its PID so the agent can hang the shell up when the terminal closes.
func shellCommand(shell string) []string {
	switch shell {
	case protocol.ShellBash:
		return []string{"/bin/sh", "-c", announcePID + "exec /bin/bash"}
	case protocol.ShellSh:
		return []string{"/bin/sh", "-c", announcePID + "exec /bin/sh"}
	}
	return []string{"/bin/sh", "-c", announcePID + "command -v bash >/dev/null 2>&1 && exec bash || exec sh"}
}

// StartExec opens a terminal in a running container. The container reference and
// every option are validated first, and the only command ever run is the chosen shell.
func (c *Client) StartExec(ctx context.Context, p protocol.DockerExecParams) (*ExecSession, error) {
	if err := checkRef(p.ID); err != nil {
		return nil, err
	}
	if err := p.Normalize(); err != nil {
		return nil, ErrBadRequest
	}
	ctx, cancel := context.WithTimeout(ctx, execSetupTimeout)
	defer cancel()

	create := map[string]any{
		"AttachStdin": true, "AttachStdout": true, "AttachStderr": true, "Tty": true,
		"Cmd": shellCommand(p.Shell), "Env": []string{"TERM=xterm-256color", "COLORTERM=truecolor"},
	}
	if p.User != "" {
		create["User"] = p.User
	}
	body, err := json.Marshal(create)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, http.MethodPost, containerPath(p.ID, "/exec"), nil, body)
	if err != nil {
		return nil, err
	}
	var created struct {
		ID string `json:"Id"`
	}
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&created)
	resp.Body.Close()
	if decodeErr != nil || created.ID == "" {
		return nil, errors.New("docker did not return the exec ID")
	}

	conn, reader, err := c.hijack(ctx, "/exec/"+url.PathEscape(created.ID)+"/start",
		map[string]any{"Detach": false, "Tty": true, "ConsoleSize": []uint16{p.Rows, p.Cols}})
	if err != nil {
		return nil, err
	}
	return &ExecSession{client: c, execID: created.ID, containerID: p.ID, user: p.User, conn: conn, reader: reader}, nil
}

// hijack sends an upgrade request and returns the raw connection and a reader that
// includes any bytes already buffered behind the response headers.
func (c *Client) hijack(ctx context.Context, path string, payload any) (net.Conn, *bufio.Reader, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, err
	}
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, nil, err
	}
	fail := func(err error) (net.Conn, *bufio.Reader, error) { conn.Close(); return nil, nil, err }
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://docker/"+apiVersion+path, bytes.NewReader(body))
	if err != nil {
		return fail(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "tcp")
	if err := req.Write(conn); err != nil {
		return fail(err)
	}
	reader := bufio.NewReaderSize(conn, 64<<10)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		return fail(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols && resp.StatusCode != http.StatusOK {
		var failure struct {
			Message string `json:"message"`
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		_ = json.Unmarshal(data, &failure)
		if len(failure.Message) > 300 {
			failure.Message = failure.Message[:300]
		}
		return fail(&APIError{Status: resp.StatusCode, Message: strings.TrimSpace(failure.Message)})
	}
	_ = conn.SetDeadline(time.Time{}) // the stream itself is bounded by the caller's context
	return conn, reader, nil
}

// Read returns terminal output. The shell's PID announcement, which arrives first,
// is consumed here and never reaches the caller.
func (s *ExecSession) Read(p []byte) (int, error) {
	s.mu.Lock()
	scanned := s.scanned
	s.mu.Unlock()
	if scanned {
		n, err := s.drain(p)
		if errors.Is(err, io.EOF) {
			s.markEnded()
		}
		return n, err
	}
	return s.readAnnouncement(p)
}

// drain hands out output held back while looking for the announcement, then reads on.
func (s *ExecSession) drain(p []byte) (int, error) {
	s.mu.Lock()
	if len(s.pending) > 0 {
		n := copy(p, s.pending)
		s.pending = s.pending[n:]
		s.mu.Unlock()
		return n, nil
	}
	s.mu.Unlock()
	return s.reader.Read(p)
}

// readAnnouncement reads the first output, removes the PID announcement if present,
// and releases whatever else arrived with it. It keeps reading only while the output
// could still turn out to contain a partly received announcement.
func (s *ExecSession) readAnnouncement(p []byte) (int, error) {
	buffer := make([]byte, 4096)
	for {
		n, readErr := s.reader.Read(buffer)
		s.mu.Lock()
		s.pending = append(s.pending, buffer[:n]...)
		done := s.extractAnnouncement() || readErr != nil
		if done {
			s.scanned = true
		}
		held := len(s.pending)
		s.mu.Unlock()
		if !done {
			continue
		}
		if held > 0 {
			// Data first; a read error, if any, is reported again by the next Read.
			return s.drain(p)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				s.markEnded()
			}
			return 0, readErr
		}
		return s.drain(p) // the announcement was the whole read; carry on with ordinary output
	}
}

// extractAnnouncement looks for the PID announcement in the held-back output and
// removes it. It reports whether scanning is finished, which is when the
// announcement was found, or cannot be coming. The caller holds s.mu.
func (s *ExecSession) extractAnnouncement() bool {
	data := s.pending
	start := bytes.Index(data, []byte(pidPrefix))
	if start < 0 {
		// Nothing yet. Keep waiting only while the output is still a possible beginning of it.
		return !(len(data) < len(pidPrefix) && bytes.HasPrefix([]byte(pidPrefix), data))
	}
	end := bytes.Index(data[start:], []byte(pidSuffix))
	if end < 0 {
		return len(data)-start >= pidScanLimit // an unfinished announcement: wait, up to a limit
	}
	if pid, err := strconv.Atoi(string(data[start+len(pidPrefix) : start+end])); err == nil && pid > 1 {
		s.pid = pid
	}
	s.pending = append(append([]byte(nil), data[:start]...), data[start+end+len(pidSuffix):]...)
	return true
}

func (s *ExecSession) markEnded() {
	s.mu.Lock()
	s.ended = true
	s.mu.Unlock()
}

// Write sends keystrokes to the terminal.
func (s *ExecSession) Write(p []byte) (int, error) { return s.conn.Write(p) }

// Close ends the terminal: it drops the connection and, unless the shell already
// exited on its own, hangs the shell up, because Docker leaves the process running
// when its client disconnects.
func (s *ExecSession) Close() error {
	err := s.conn.Close()
	s.mu.Lock()
	pid, ended := s.pid, s.ended
	s.pid = 0
	s.mu.Unlock()
	if pid > 1 && !ended {
		s.hangUp(pid)
	}
	return err
}

// hangUp signals the shell from a second, detached exec: SIGHUP first, which makes a
// shell and its foreground jobs exit as they would when a terminal closes, then SIGKILL
// a moment later for a shell that ignores it. It runs as the same user as the terminal.
func (s *ExecSession) hangUp(pid int) {
	ctx, cancel := context.WithTimeout(context.Background(), execSetupTimeout)
	defer cancel()
	script := fmt.Sprintf("kill -HUP %d 2>/dev/null; sleep 1; kill -KILL %d 2>/dev/null; exit 0", pid, pid)
	create := map[string]any{"AttachStdin": false, "AttachStdout": false, "AttachStderr": false, "Tty": false, "Cmd": []string{"/bin/sh", "-c", script}}
	if s.user != "" {
		create["User"] = s.user
	}
	body, err := json.Marshal(create)
	if err != nil {
		return
	}
	resp, err := s.client.do(ctx, http.MethodPost, containerPath(s.containerID, "/exec"), nil, body)
	if err != nil {
		return
	}
	var created struct {
		ID string `json:"Id"`
	}
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&created)
	resp.Body.Close()
	if decodeErr != nil || created.ID == "" {
		return
	}
	start, err := json.Marshal(map[string]any{"Detach": true, "Tty": false})
	if err != nil {
		return
	}
	if resp, err := s.client.do(ctx, http.MethodPost, "/exec/"+url.PathEscape(created.ID)+"/start", nil, start); err == nil {
		resp.Body.Close()
	}
}

// Resize changes the terminal size, clamped to the supported range.
func (s *ExecSession) Resize(ctx context.Context, cols, rows uint16) error {
	cols, rows = protocol.ClampExecSize(cols, protocol.DefaultExecCols), protocol.ClampExecSize(rows, protocol.DefaultExecRows)
	ctx, cancel := context.WithTimeout(ctx, execInfoTimeout)
	defer cancel()
	resp, err := s.client.do(ctx, http.MethodPost, "/exec/"+url.PathEscape(s.execID)+"/resize", url.Values{"w": {strconv.Itoa(int(cols))}, "h": {strconv.Itoa(int(rows))}}, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// ExitCode reports how the shell ended. Docker may need a moment after the stream
// closes before it records the exit, so it is polled briefly.
func (s *ExecSession) ExitCode(ctx context.Context) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, execInfoTimeout)
	defer cancel()
	for {
		var info struct {
			Running  bool `json:"Running"`
			ExitCode int  `json:"ExitCode"`
		}
		if err := s.client.getJSON(ctx, "/exec/"+url.PathEscape(s.execID)+"/json", nil, 1<<20, &info); err != nil {
			return 0, err
		}
		if !info.Running {
			return info.ExitCode, nil
		}
		select {
		case <-ctx.Done():
			return 0, fmt.Errorf("the shell has not reported an exit status: %w", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}
