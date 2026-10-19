// Package channel is the agent's side of the persistent server channel. The
// agent dials out over WebSocket, announces itself, then serves requests from the
// server. It never listens for inbound connections.
package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/deranjer/vigara/internal/protocol"
	"github.com/deranjer/vigara/internal/serverurl"
)

// Handler answers one call. The returned value is JSON encoded as the result.
type Handler func(ctx context.Context, params json.RawMessage) (any, error)

// StreamHandler produces a stream by calling send for each chunk until it returns.
type StreamHandler func(ctx context.Context, params json.RawMessage, send func([]byte) error) error

const (
	helloTimeout     = 10 * time.Second
	writeTimeout     = 10 * time.Second
	maxConcurrent    = 64
	defaultMinWait   = time.Second
	defaultMaxWait   = 60 * time.Second
	unsupportedWait  = 5 * time.Minute
	stableConnection = 30 * time.Second
)

// Client maintains the channel to one server.
type Client struct {
	ServerURL                string
	Credential               string
	AgentVersion             string
	AllowInsecurePrivateHTTP bool
	// Capabilities reports what the local operator enabled; it decides the Docker tier.
	Capabilities func() []string
	Calls        map[string]Handler
	Streams      map[string]StreamHandler
	// MethodTier overrides the built-in method table; tests use it.
	MethodTier func(method string) (tier string, known bool)
	Logger     *slog.Logger
	// MinBackoff and MaxBackoff bound reconnect delays; zero values use defaults.
	MinBackoff, MaxBackoff time.Duration
}

// NewClient returns a Client serving the built-in methods.
func NewClient(serverURL, credential, agentVersion string, allowInsecurePrivateHTTP bool, capabilities func() []string, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	c := &Client{ServerURL: serverURL, Credential: credential, AgentVersion: agentVersion, AllowInsecurePrivateHTTP: allowInsecurePrivateHTTP,
		Capabilities: capabilities, Calls: map[string]Handler{}, Streams: map[string]StreamHandler{}, Logger: logger}
	c.Calls[protocol.MethodPing] = func(context.Context, json.RawMessage) (any, error) {
		return protocol.ChannelPingResult{Pong: true, AgentVersion: agentVersion}, nil
	}
	c.Calls[protocol.MethodInfo] = func(context.Context, json.RawMessage) (any, error) {
		caps := c.capabilities()
		return protocol.ChannelInfoResult{AgentVersion: agentVersion, DockerTier: protocol.DockerTier(caps), Capabilities: caps}, nil
	}
	return c
}

func (c *Client) capabilities() []string {
	if c.Capabilities == nil {
		return []string{}
	}
	if caps := c.Capabilities(); caps != nil {
		return caps
	}
	return []string{}
}

func (c *Client) methodTier(method string) (string, bool) {
	if c.MethodTier != nil {
		return c.MethodTier(method)
	}
	return protocol.ChannelMethodTier(method)
}

// WebSocketURL converts the validated server URL into the channel endpoint.
func WebSocketURL(serverURL string, allowInsecurePrivateHTTP bool) (string, error) {
	if err := serverurl.Validate(serverURL, allowInsecurePrivateHTTP); err != nil {
		return "", err
	}
	u, err := url.Parse(serverURL)
	if err != nil {
		return "", err
	}
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = strings.TrimRight(u.Path, "/") + protocol.ChannelPath
	return u.String(), nil
}

// Run keeps the channel connected until ctx ends, reconnecting with jittered
// backoff. A server without channel support is retried only occasionally, and a
// rejected credential is retried slowly rather than hammering the server.
func (c *Client) Run(ctx context.Context) {
	minWait, maxWait := c.MinBackoff, c.MaxBackoff
	if minWait <= 0 {
		minWait = defaultMinWait
	}
	if maxWait <= 0 {
		maxWait = defaultMaxWait
	}
	wait := minWait
	for ctx.Err() == nil {
		started := time.Now()
		unsupported, err := c.session(ctx)
		if ctx.Err() != nil {
			return
		}
		switch {
		case unsupported:
			c.Logger.Info("server does not offer the live channel; live features are unavailable until it does", "retryIn", unsupportedWait.String())
			wait = unsupportedWait
		case err != nil:
			c.Logger.Warn("channel disconnected", "error", err.Error())
		}
		if time.Since(started) >= stableConnection {
			wait = minWait
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait + time.Duration(rand.Int64N(int64(wait)/2+1))):
		}
		if !unsupported && wait < maxWait {
			wait = min(wait*2, maxWait)
		}
	}
}

// session runs one connection. unsupported is true when the server answered the
// upgrade with a status showing it has no channel endpoint or refuses this agent.
func (c *Client) session(ctx context.Context) (unsupported bool, err error) {
	endpoint, err := WebSocketURL(c.ServerURL, c.AllowInsecurePrivateHTTP)
	if err != nil {
		return false, err
	}
	httpClient := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	dialCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	ws, resp, err := websocket.Dial(dialCtx, endpoint, &websocket.DialOptions{
		HTTPClient: httpClient, HTTPHeader: http.Header{"Authorization": []string{"Bearer " + c.Credential}},
	})
	cancel()
	if err != nil {
		if resp != nil {
			switch resp.StatusCode {
			case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusUnauthorized, http.StatusForbidden:
				return true, nil
			}
			return false, fmt.Errorf("server refused the channel (HTTP %d)", resp.StatusCode)
		}
		return false, fmt.Errorf("connect: %w", err)
	}
	ws.SetReadLimit(protocol.MaxChannelFrameBytes)
	conn := &session{client: c, ws: ws, cancels: map[string]context.CancelFunc{}}
	defer ws.CloseNow()

	hello := protocol.ChannelFrame{Type: protocol.FrameHello, Version: protocol.ChannelVersion, AgentVersion: c.AgentVersion, Capabilities: c.capabilities()}
	if err := conn.write(ctx, hello); err != nil {
		return false, fmt.Errorf("send hello: %w", err)
	}
	welcomeCtx, cancelWelcome := context.WithTimeout(ctx, helloTimeout)
	kind, data, err := ws.Read(welcomeCtx)
	cancelWelcome()
	var welcome protocol.ChannelFrame
	if err != nil || kind != websocket.MessageText || json.Unmarshal(data, &welcome) != nil || welcome.Type != protocol.FrameWelcome {
		return false, errors.New("server did not accept the channel")
	}
	c.Logger.Info("channel connected")

	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	return false, conn.serve(runCtx)
}

type session struct {
	client  *Client
	ws      *websocket.Conn
	writeMu sync.Mutex

	mu      sync.Mutex
	cancels map[string]context.CancelFunc
	active  int
	wg      sync.WaitGroup
}

func (s *session) write(ctx context.Context, frame protocol.ChannelFrame) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.ws.Write(ctx, websocket.MessageText, data)
}

func (s *session) serve(ctx context.Context) error {
	defer func() {
		s.mu.Lock()
		for _, cancel := range s.cancels {
			cancel()
		}
		s.mu.Unlock()
		s.wg.Wait()
	}()
	for {
		kind, data, err := s.ws.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				_ = s.ws.Close(websocket.StatusNormalClosure, "agent stopping")
				return nil
			}
			return err
		}
		if kind != websocket.MessageText {
			return errors.New("unexpected binary frame")
		}
		var frame protocol.ChannelFrame
		if json.Unmarshal(data, &frame) != nil {
			return errors.New("invalid frame from server")
		}
		switch frame.Type {
		case protocol.FrameCall, protocol.FrameStreamOpen:
			s.start(ctx, frame)
		case protocol.FrameCancel:
			s.mu.Lock()
			cancel := s.cancels[frame.ID]
			s.mu.Unlock()
			if cancel != nil {
				cancel()
			}
		}
	}
}

// reject answers a request that cannot run.
func (s *session) reject(ctx context.Context, frame protocol.ChannelFrame, code, message string) {
	reply := protocol.ChannelFrame{Type: protocol.FrameResult, ID: frame.ID, Error: &protocol.ChannelError{Code: code, Message: message}}
	if frame.Type == protocol.FrameStreamOpen {
		reply.Type = protocol.FrameStreamEnd
	}
	_ = s.write(ctx, reply)
}

func (s *session) start(ctx context.Context, frame protocol.ChannelFrame) {
	c := s.client
	tier, known := c.methodTier(frame.Method)
	if !known {
		s.reject(ctx, frame, protocol.ErrCodeUnknownMethod, "unknown method")
		return
	}
	// The local operator's setting is the authority: refuse above it even if the server asked.
	if err := protocol.CheckChannelTier(tier, protocol.DockerTier(c.capabilities())); err != nil {
		var channelErr *protocol.ChannelError
		errors.As(err, &channelErr)
		s.reject(ctx, frame, channelErr.Code, channelErr.Message)
		return
	}
	var call Handler
	var stream StreamHandler
	if frame.Type == protocol.FrameStreamOpen {
		if stream = c.Streams[frame.Method]; stream == nil {
			s.reject(ctx, frame, protocol.ErrCodeUnknownMethod, "method is not a stream")
			return
		}
	} else if call = c.Calls[frame.Method]; call == nil {
		s.reject(ctx, frame, protocol.ErrCodeUnknownMethod, "method is not available on this agent")
		return
	}
	s.mu.Lock()
	if s.active >= maxConcurrent {
		s.mu.Unlock()
		s.reject(ctx, frame, protocol.ErrCodeBusy, "too many concurrent requests")
		return
	}
	jobCtx, cancel := context.WithCancel(ctx)
	if frame.DeadlineMillis > 0 {
		jobCtx, cancel = context.WithTimeout(ctx, time.Duration(frame.DeadlineMillis)*time.Millisecond)
	}
	s.cancels[frame.ID] = cancel
	s.active++
	s.wg.Add(1)
	s.mu.Unlock()

	go func() {
		defer func() {
			cancel()
			s.mu.Lock()
			delete(s.cancels, frame.ID)
			s.active--
			s.mu.Unlock()
			s.wg.Done()
		}()
		if stream != nil {
			s.runStream(jobCtx, ctx, frame, stream)
			return
		}
		s.runCall(jobCtx, ctx, frame, call)
	}()
}

func failure(ctx context.Context, err error) *protocol.ChannelError {
	var channelErr *protocol.ChannelError
	switch {
	case errors.As(err, &channelErr):
		return channelErr
	case errors.Is(err, context.DeadlineExceeded):
		return &protocol.ChannelError{Code: protocol.ErrCodeTimeout, Message: "the request timed out on the agent"}
	case errors.Is(err, context.Canceled) || ctx.Err() != nil:
		return &protocol.ChannelError{Code: protocol.ErrCodeCancelled, Message: "the request was cancelled"}
	}
	// Handler errors may carry host details; report only a generic message.
	return &protocol.ChannelError{Code: protocol.ErrCodeInternal, Message: "the agent could not complete the request"}
}

func (s *session) runCall(jobCtx, connCtx context.Context, frame protocol.ChannelFrame, call Handler) {
	reply := protocol.ChannelFrame{Type: protocol.FrameResult, ID: frame.ID}
	func() {
		defer func() {
			if recover() != nil {
				reply.Error = &protocol.ChannelError{Code: protocol.ErrCodeInternal, Message: "the agent could not complete the request"}
			}
		}()
		result, err := call(jobCtx, frame.Params)
		if err != nil {
			s.client.Logger.Warn("channel call failed", "method", frame.Method, "error", err.Error())
			reply.Error = failure(jobCtx, err)
			return
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			reply.Error = &protocol.ChannelError{Code: protocol.ErrCodeInternal, Message: "the result could not be encoded"}
			return
		}
		reply.Result = encoded
	}()
	if jobCtx.Err() != nil && reply.Error == nil && connCtx.Err() != nil {
		return
	}
	_ = s.write(connCtx, reply)
}

func (s *session) runStream(jobCtx, connCtx context.Context, frame protocol.ChannelFrame, stream StreamHandler) {
	var seq uint64
	send := func(data []byte) error {
		for len(data) > 0 {
			chunk := data
			if len(chunk) > protocol.MaxStreamChunkBytes {
				chunk = chunk[:protocol.MaxStreamChunkBytes]
			}
			data = data[len(chunk):]
			seq++
			if err := jobCtx.Err(); err != nil {
				return err
			}
			if err := s.write(jobCtx, protocol.ChannelFrame{Type: protocol.FrameStreamData, ID: frame.ID, Seq: seq, Data: chunk}); err != nil {
				return err
			}
		}
		return nil
	}
	end := protocol.ChannelFrame{Type: protocol.FrameStreamEnd, ID: frame.ID}
	func() {
		defer func() {
			if recover() != nil {
				end.Error = &protocol.ChannelError{Code: protocol.ErrCodeInternal, Message: "the agent could not complete the request"}
			}
		}()
		if err := stream(jobCtx, frame.Params, send); err != nil && !(errors.Is(err, context.Canceled) && jobCtx.Err() != nil && connCtx.Err() == nil) {
			s.client.Logger.Warn("channel stream failed", "method", frame.Method, "error", err.Error())
			end.Error = failure(jobCtx, err)
		}
	}()
	// A cancelled stream was already abandoned by the server; nothing is waiting for its end.
	if end.Error != nil && end.Error.Code == protocol.ErrCodeCancelled {
		return
	}
	_ = s.write(connCtx, end)
}
