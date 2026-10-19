package protocol

import (
	"encoding/json"
	"fmt"
)

// ChannelVersion is the wire version of the persistent agent channel. The agent
// dials the server over WebSocket (never the reverse) and both sides exchange
// ChannelFrame values as JSON text messages.
const ChannelVersion = "v1"

// ChannelPath is the server endpoint agents connect to.
const ChannelPath = "/api/agent/channel"

// Frame types.
const (
	FrameHello      = "hello"       // agent -> server, first frame
	FrameWelcome    = "welcome"     // server -> agent, accepts the hello
	FrameCall       = "call"        // server -> agent, one request expecting one result
	FrameResult     = "result"      // agent -> server, answers a call
	FrameStreamOpen = "stream_open" // server -> agent, starts a stream
	FrameStreamData = "stream_data" // agent -> server, one ordered chunk
	FrameStreamEnd  = "stream_end"  // agent -> server, the stream finished (Error set on failure)
	FrameCancel     = "cancel"      // server -> agent, abandon a call or stream
)

// Error codes carried in ChannelError.
const (
	ErrCodeUnknownMethod    = "unknown_method"
	ErrCodeTierInsufficient = "tier_insufficient"
	ErrCodeBadRequest       = "bad_request"
	ErrCodeTimeout          = "timeout"
	ErrCodeCancelled        = "cancelled"
	ErrCodeSlowConsumer     = "slow_consumer"
	ErrCodeInternal         = "internal"
	ErrCodeBusy             = "busy"
)

// ChannelError is a structured failure. Messages must not contain secrets.
type ChannelError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *ChannelError) Error() string { return e.Code + ": " + e.Message }

// ChannelFrame is the only message shape on the channel. Which fields are set
// depends on Type; unknown fields must be ignored so v1 can grow.
type ChannelFrame struct {
	Type    string          `json:"type"`
	ID      string          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Data    []byte          `json:"data,omitempty"`
	Seq     uint64          `json:"seq,omitempty"`
	Error   *ChannelError   `json:"error,omitempty"`
	Version string          `json:"version,omitempty"`
	// DeadlineMillis bounds a call or stream on the agent; zero means the agent's default.
	DeadlineMillis int64 `json:"deadlineMillis,omitempty"`
	// Hello fields.
	AgentVersion string   `json:"agentVersion,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

// Limits shared by both ends.
const (
	MaxChannelFrameBytes = 1 << 20
	MaxStreamChunkBytes  = 32 << 10
)

// Channel methods and the Docker tier each requires. An empty tier means no
// Docker access. Both server and agent refuse a method above the agent's tier,
// and refuse methods that are not listed here.
const (
	MethodPing = "channel.ping"
	MethodInfo = "channel.info"
)

var channelMethodTiers = map[string]string{
	MethodPing: "",
	MethodInfo: "",
}

// ChannelMethodTier returns the tier a built-in method requires and whether the
// method is known.
func ChannelMethodTier(method string) (tier string, known bool) {
	tier, known = channelMethodTiers[method]
	return tier, known
}

// CheckChannelTier reports whether an agent granted `granted` may run a method
// requiring `required`. An empty required tier is always allowed.
func CheckChannelTier(required, granted string) error {
	if required == "" || DockerTierRank(granted) >= DockerTierRank(required) {
		return nil
	}
	return &ChannelError{Code: ErrCodeTierInsufficient, Message: fmt.Sprintf("this host allows Docker %s access, but the request needs %s", granted, required)}
}

// ChannelPingResult is the reply to MethodPing.
type ChannelPingResult struct {
	Pong         bool   `json:"pong"`
	AgentVersion string `json:"agentVersion"`
}

// ChannelInfoResult is the reply to MethodInfo.
type ChannelInfoResult struct {
	AgentVersion string   `json:"agentVersion"`
	DockerTier   string   `json:"dockerTier"`
	Capabilities []string `json:"capabilities"`
}
