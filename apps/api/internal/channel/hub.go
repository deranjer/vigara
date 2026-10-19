// Package channel runs the persistent, agent-initiated WebSocket that carries
// live requests (inspect, logs, actions, terminals) between the server and
// agents. Agents always dial out; the server never connects to an agent.
package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/deranjer/vigara/internal/protocol"
)

var (
	// ErrAgentOffline means no channel is currently connected for the agent.
	ErrAgentOffline = errors.New("agent is not connected")
	// ErrAgentDisconnected means the channel dropped while a request was in flight.
	ErrAgentDisconnected = errors.New("agent disconnected")
)

const (
	defaultMaxInFlight  = 32
	defaultPingInterval = 20 * time.Second
	helloTimeout        = 10 * time.Second
	writeTimeout        = 10 * time.Second
	streamBuffer        = 256
)

// Options configures a Hub.
type Options struct {
	// AuthenticateAgent resolves the agent behind a channel request from its bearer credential.
	AuthenticateAgent func(r *http.Request) (agentID string, err error)
	// Capabilities returns the capabilities the server has accepted for the agent.
	// They are the enrollment-scoped truth: an agent's own hello cannot raise them.
	Capabilities func(ctx context.Context, agentID string) ([]string, error)
	// MethodTier overrides the built-in method table; tests use it.
	MethodTier func(method string) (tier string, known bool)
	// MaxInFlight bounds concurrent calls plus streams per agent.
	MaxInFlight  int
	PingInterval time.Duration
}

// Hub tracks the connected agents and routes requests to them.
type Hub struct {
	opts  Options
	mu    sync.RWMutex
	conns map[string]*agentConn
}

// NewHub returns a Hub with defaults applied.
func NewHub(opts Options) *Hub {
	if opts.MaxInFlight <= 0 {
		opts.MaxInFlight = defaultMaxInFlight
	}
	if opts.PingInterval <= 0 {
		opts.PingInterval = defaultPingInterval
	}
	if opts.MethodTier == nil {
		opts.MethodTier = protocol.ChannelMethodTier
	}
	return &Hub{opts: opts, conns: map[string]*agentConn{}}
}

// AgentInfo describes one connected agent.
type AgentInfo struct {
	AgentID      string    `json:"agentId"`
	DockerTier   string    `json:"dockerTier"`
	AgentVersion string    `json:"agentVersion"`
	ConnectedAt  time.Time `json:"connectedAt"`
}

// Agents lists connected agents ordered by ID.
func (h *Hub) Agents() []AgentInfo {
	h.mu.RLock()
	defer h.mu.RUnlock()
	infos := make([]AgentInfo, 0, len(h.conns))
	for _, c := range h.conns {
		infos = append(infos, c.info())
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].AgentID < infos[j].AgentID })
	return infos
}

// Agent returns one connected agent.
func (h *Hub) Agent(agentID string) (AgentInfo, bool) {
	h.mu.RLock()
	c := h.conns[agentID]
	h.mu.RUnlock()
	if c == nil {
		return AgentInfo{}, false
	}
	return c.info(), true
}

type agentConn struct {
	hub         *Hub
	id          string
	tier        string
	version     string
	connectedAt time.Time
	ws          *websocket.Conn
	writeMu     sync.Mutex

	mu      sync.Mutex
	nextID  uint64
	pending map[string]chan protocol.ChannelFrame
	streams map[string]*Stream
	done    chan struct{}
	once    sync.Once
}

func (c *agentConn) info() AgentInfo {
	return AgentInfo{AgentID: c.id, DockerTier: c.tier, AgentVersion: c.version, ConnectedAt: c.connectedAt}
}

func (c *agentConn) write(ctx context.Context, frame protocol.ChannelFrame) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.ws.Write(ctx, websocket.MessageText, data)
}

// close ends the connection once and fails everything still waiting.
func (c *agentConn) close(status websocket.StatusCode, reason string) {
	c.once.Do(func() {
		close(c.done)
		_ = c.ws.Close(status, reason)
		c.mu.Lock()
		streams := make([]*Stream, 0, len(c.streams))
		for _, s := range c.streams {
			streams = append(streams, s)
		}
		c.streams = map[string]*Stream{}
		c.mu.Unlock()
		for _, s := range streams {
			s.finish(ErrAgentDisconnected)
		}
	})
}

func (c *agentConn) reserve() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pending)+len(c.streams) >= c.hub.opts.MaxInFlight {
		return "", &protocol.ChannelError{Code: protocol.ErrCodeBusy, Message: "too many requests are in flight for this agent"}
	}
	c.nextID++
	return strconv.FormatUint(c.nextID, 10), nil
}

func (h *Hub) conn(agentID string) (*agentConn, error) {
	h.mu.RLock()
	c := h.conns[agentID]
	h.mu.RUnlock()
	if c == nil {
		return nil, ErrAgentOffline
	}
	return c, nil
}

func (h *Hub) authorizeMethod(c *agentConn, method string) error {
	tier, known := h.opts.MethodTier(method)
	if !known {
		return &protocol.ChannelError{Code: protocol.ErrCodeUnknownMethod, Message: "unknown method " + method}
	}
	return protocol.CheckChannelTier(tier, c.tier)
}

func deadlineMillis(ctx context.Context) int64 {
	if deadline, ok := ctx.Deadline(); ok {
		if ms := time.Until(deadline).Milliseconds(); ms > 0 {
			return ms
		}
		return 1
	}
	return 0
}

// Call sends one request to an agent and waits for its result, decoding it into
// out when out is non-nil. The context's deadline is forwarded to the agent.
// The method must be known and within the agent's Docker tier.
func (h *Hub) Call(ctx context.Context, agentID, method string, params, out any) error {
	c, err := h.conn(agentID)
	if err != nil {
		return err
	}
	if err := h.authorizeMethod(c, method); err != nil {
		return err
	}
	var raw json.RawMessage
	if params != nil {
		if raw, err = json.Marshal(params); err != nil {
			return &protocol.ChannelError{Code: protocol.ErrCodeBadRequest, Message: "parameters cannot be encoded"}
		}
	}
	id, err := c.reserve()
	if err != nil {
		return err
	}
	reply := make(chan protocol.ChannelFrame, 1)
	c.mu.Lock()
	c.pending[id] = reply
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()

	if err := c.write(ctx, protocol.ChannelFrame{Type: protocol.FrameCall, ID: id, Method: method, Params: raw, DeadlineMillis: deadlineMillis(ctx)}); err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	select {
	case frame := <-reply:
		if frame.Error != nil {
			return frame.Error
		}
		if out != nil && len(frame.Result) > 0 {
			if err := json.Unmarshal(frame.Result, out); err != nil {
				return fmt.Errorf("decode result: %w", err)
			}
		}
		return nil
	case <-ctx.Done():
		c.cancel(id)
		return ctx.Err()
	case <-c.done:
		return ErrAgentDisconnected
	}
}

func (c *agentConn) cancel(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = c.write(ctx, protocol.ChannelFrame{Type: protocol.FrameCancel, ID: id})
}

// Stream is an ordered sequence of byte chunks from an agent. Read from C until
// it closes, then check Err. Call Close to abandon it early.
type Stream struct {
	C <-chan []byte

	conn   *agentConn
	id     string
	mu     sync.Mutex
	ch     chan []byte
	closed bool
	err    error
}

// Err reports why the stream ended; it is nil for a clean end and valid once C is closed.
func (s *Stream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *Stream) finish(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed, s.err = true, err
	close(s.ch)
}

func (s *Stream) deliver(data []byte) (overflow bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	select {
	case s.ch <- data:
		return false
	default:
		return true
	}
}

// Close abandons the stream and tells the agent to stop producing it.
func (s *Stream) Close() {
	s.conn.mu.Lock()
	_, active := s.conn.streams[s.id]
	delete(s.conn.streams, s.id)
	s.conn.mu.Unlock()
	if active {
		s.conn.cancel(s.id)
	}
	s.finish(&protocol.ChannelError{Code: protocol.ErrCodeCancelled, Message: "stream closed"})
}

// OpenStream starts a stream method on an agent. A consumer that falls more than
// a bounded number of chunks behind is cut off with a slow-consumer error rather
// than letting memory grow.
func (h *Hub) OpenStream(ctx context.Context, agentID, method string, params any) (*Stream, error) {
	c, err := h.conn(agentID)
	if err != nil {
		return nil, err
	}
	if err := h.authorizeMethod(c, method); err != nil {
		return nil, err
	}
	var raw json.RawMessage
	if params != nil {
		if raw, err = json.Marshal(params); err != nil {
			return nil, &protocol.ChannelError{Code: protocol.ErrCodeBadRequest, Message: "parameters cannot be encoded"}
		}
	}
	id, err := c.reserve()
	if err != nil {
		return nil, err
	}
	ch := make(chan []byte, streamBuffer)
	s := &Stream{C: ch, ch: ch, conn: c, id: id}
	c.mu.Lock()
	c.streams[id] = s
	c.mu.Unlock()
	if err := c.write(ctx, protocol.ChannelFrame{Type: protocol.FrameStreamOpen, ID: id, Method: method, Params: raw, DeadlineMillis: deadlineMillis(ctx)}); err != nil {
		c.mu.Lock()
		delete(c.streams, id)
		c.mu.Unlock()
		s.finish(err)
		return nil, fmt.Errorf("open stream: %w", err)
	}
	return s, nil
}

func (c *agentConn) readLoop(ctx context.Context) {
	for {
		kind, data, err := c.ws.Read(ctx)
		if err != nil {
			c.close(websocket.StatusNormalClosure, "")
			return
		}
		if kind != websocket.MessageText {
			c.close(websocket.StatusUnsupportedData, "text frames only")
			return
		}
		var frame protocol.ChannelFrame
		if json.Unmarshal(data, &frame) != nil {
			c.close(websocket.StatusProtocolError, "invalid frame")
			return
		}
		switch frame.Type {
		case protocol.FrameResult:
			c.mu.Lock()
			reply := c.pending[frame.ID]
			c.mu.Unlock()
			if reply != nil {
				select {
				case reply <- frame:
				default:
				}
			}
		case protocol.FrameStreamData:
			c.mu.Lock()
			s := c.streams[frame.ID]
			c.mu.Unlock()
			if s != nil && s.deliver(frame.Data) {
				c.mu.Lock()
				delete(c.streams, frame.ID)
				c.mu.Unlock()
				c.cancel(frame.ID)
				s.finish(&protocol.ChannelError{Code: protocol.ErrCodeSlowConsumer, Message: "the consumer fell too far behind"})
			}
		case protocol.FrameStreamEnd:
			c.mu.Lock()
			s := c.streams[frame.ID]
			delete(c.streams, frame.ID)
			c.mu.Unlock()
			if s != nil {
				var endErr error
				if frame.Error != nil {
					endErr = frame.Error
				}
				s.finish(endErr)
			}
		default:
			c.close(websocket.StatusProtocolError, "unexpected frame")
			return
		}
	}
}

func (c *agentConn) pingLoop(ctx context.Context) {
	ticker := time.NewTicker(c.hub.opts.PingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.ws.Ping(pingCtx)
			cancel()
			if err != nil {
				c.close(websocket.StatusGoingAway, "ping timeout")
				return
			}
		}
	}
}

// ServeAgent upgrades an agent's channel request and serves it until it closes.
func (h *Hub) ServeAgent(w http.ResponseWriter, r *http.Request) {
	agentID, err := h.opts.AuthenticateAgent(r)
	if err != nil || agentID == "" {
		http.Error(w, "agent credential required", http.StatusUnauthorized)
		return
	}
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return // Accept has already written the response.
	}
	ws.SetReadLimit(protocol.MaxChannelFrameBytes)
	ctx := r.Context()

	helloCtx, cancel := context.WithTimeout(ctx, helloTimeout)
	kind, data, err := ws.Read(helloCtx)
	cancel()
	var hello protocol.ChannelFrame
	if err != nil || kind != websocket.MessageText || json.Unmarshal(data, &hello) != nil || hello.Type != protocol.FrameHello {
		_ = ws.Close(websocket.StatusProtocolError, "hello required")
		return
	}
	if hello.Version != protocol.ChannelVersion {
		_ = ws.Close(websocket.StatusPolicyViolation, "unsupported channel version")
		return
	}
	capabilities, err := h.opts.Capabilities(ctx, agentID)
	if err != nil {
		_ = ws.Close(websocket.StatusInternalError, "could not read agent capabilities")
		return
	}
	c := &agentConn{
		hub: h, id: agentID, tier: protocol.DockerTier(capabilities), version: clip(hello.AgentVersion, 64), connectedAt: time.Now().UTC(),
		ws: ws, pending: map[string]chan protocol.ChannelFrame{}, streams: map[string]*Stream{}, done: make(chan struct{}),
	}
	if err := c.write(ctx, protocol.ChannelFrame{Type: protocol.FrameWelcome, Version: protocol.ChannelVersion}); err != nil {
		c.close(websocket.StatusInternalError, "")
		return
	}
	h.mu.Lock()
	previous := h.conns[agentID]
	h.conns[agentID] = c
	h.mu.Unlock()
	if previous != nil {
		previous.close(websocket.StatusPolicyViolation, "replaced by a newer connection")
	}
	defer func() {
		c.close(websocket.StatusNormalClosure, "")
		h.mu.Lock()
		if h.conns[agentID] == c {
			delete(h.conns, agentID)
		}
		h.mu.Unlock()
	}()
	go c.pingLoop(ctx)
	c.readLoop(ctx)
}

func clip(value string, limit int) string {
	if len(value) > limit {
		return value[:limit]
	}
	return value
}
