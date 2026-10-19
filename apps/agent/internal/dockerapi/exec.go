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
	client *Client
	execID string
	conn   net.Conn
	reader *bufio.Reader
}

// shellCommand returns the command a terminal runs for the chosen shell. "auto"
// prefers bash and falls back to sh, because many images only ship sh.
func shellCommand(shell string) []string {
	switch shell {
	case protocol.ShellBash:
		return []string{"/bin/bash"}
	case protocol.ShellSh:
		return []string{"/bin/sh"}
	}
	return []string{"/bin/sh", "-c", "command -v bash >/dev/null 2>&1 && exec bash || exec sh"}
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
	return &ExecSession{client: c, execID: created.ID, conn: conn, reader: reader}, nil
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

// Read returns terminal output.
func (s *ExecSession) Read(p []byte) (int, error) { return s.reader.Read(p) }

// Write sends keystrokes to the terminal.
func (s *ExecSession) Write(p []byte) (int, error) { return s.conn.Write(p) }

// Close ends the terminal. Closing the connection hangs up the shell's terminal.
func (s *ExecSession) Close() error { return s.conn.Close() }

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
