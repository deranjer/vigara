package runtime

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"

	"github.com/deranjer/vigara/internal/protocol"
)

const AgentVersion = "0.1.0"

type Client struct {
	StatePath string
	State     State
	HTTP      *http.Client
}

func NewClient(statePath string) (*Client, error) {
	state, err := Load(statePath)
	if err != nil {
		return nil, err
	}
	return &Client{StatePath: statePath, State: state, HTTP: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *Client) Enroll(ctx context.Context, serverURL, token, name string) error {
	if err := validateServerURL(serverURL); err != nil {
		return err
	}
	capability := protocol.CapabilityReport{ProtocolVersion: protocol.CurrentVersion, Platform: runtime.GOOS, Architecture: runtime.GOARCH, AgentVersion: AgentVersion, Capabilities: []string{}}
	identifiers := enrollmentIdentifiers()
	payload := enrollmentPayload(token, name, capability, identifiers)
	var result struct {
		AgentID    string `json:"agentId"`
		Credential string `json:"credential"`
	}
	if err := c.post(ctx, serverURL, "/api/agent/enroll", "", payload, &result); err != nil {
		return fmt.Errorf("enroll agent: %w", err)
	}
	if result.AgentID == "" || result.Credential == "" {
		return fmt.Errorf("server returned an incomplete agent identity")
	}
	c.State.ServerURL, c.State.AgentID, c.State.Credential = strings.TrimRight(serverURL, "/"), result.AgentID, result.Credential
	return Save(c.StatePath, c.State)
}

func enrollmentIdentifiers() []protocol.DeviceIdentifier {
	identifier, ok := platformIdentifier()
	if !ok {
		return nil
	}
	return []protocol.DeviceIdentifier{identifier}
}

func enrollmentPayload(token, name string, capability protocol.CapabilityReport, identifiers []protocol.DeviceIdentifier) map[string]any {
	payload := map[string]any{"token": token, "displayName": name, "capability": capability}
	if len(identifiers) > 0 {
		payload["identifiers"] = identifiers
	}
	return payload
}

func (c *Client) Cycle(ctx context.Context) (time.Duration, error) {
	if c.State.AgentID == "" || c.State.Credential == "" || c.State.ServerURL == "" {
		return 0, fmt.Errorf("agent is not enrolled; run enroll first")
	}
	if err := c.flush(ctx); err != nil {
		return 0, err
	}
	request := protocol.AgentRequest{ProtocolVersion: protocol.CurrentVersion, AgentID: c.State.AgentID, AgentVersion: AgentVersion, Platform: runtime.GOOS, Architecture: runtime.GOARCH, Capabilities: []string{}}
	if c.State.Configuration != nil {
		request.KnownConfigVersion = c.State.Configuration.Version
	}
	var response protocol.AgentResponse
	if err := c.post(ctx, c.State.ServerURL, "/api/agent/poll", c.State.Credential, request, &response); err != nil {
		return 0, fmt.Errorf("poll server: %w", err)
	}
	if response.ProtocolVersion != protocol.CurrentVersion {
		return 0, fmt.Errorf("server returned unsupported protocol %q", response.ProtocolVersion)
	}
	if response.Configuration != nil {
		c.State.Configuration = response.Configuration
	}
	if response.Job != nil {
		resultID, err := randomID()
		if err != nil {
			return 0, err
		}
		result := protocol.JobResult{ProtocolVersion: protocol.CurrentVersion, ID: resultID, JobID: response.Job.ID, AgentID: c.State.AgentID, ModuleID: response.Job.ModuleID, ModuleVersion: response.Job.ModuleVersion, Status: protocol.JobFailed, StartedAt: time.Now().UTC(), FinishedAt: time.Now().UTC(), Error: &protocol.Failure{Code: "unsupported_capability", Message: "This agent build does not include the requested module capability", Retryable: false}}
		if err := c.State.Enqueue(result); err != nil {
			return 0, err
		}
	}
	if err := Save(c.StatePath, c.State); err != nil {
		return 0, err
	}
	delay := time.Duration(response.PollAfterSeconds) * time.Second
	if delay < 5*time.Second {
		delay = 5 * time.Second
	}
	if delay > 300*time.Second {
		delay = 300 * time.Second
	}
	return delay + time.Duration(rand.Int64N(int64(delay/10+1))), nil
}

func (c *Client) Run(ctx context.Context) error {
	backoff := time.Second
	for {
		delay, err := c.Cycle(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			timer := time.NewTimer(backoff + time.Duration(rand.Int64N(int64(backoff/4+1))))
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-timer.C:
			}
			backoff *= 2
			if backoff > 60*time.Second {
				backoff = 60 * time.Second
			}
			continue
		}
		backoff = time.Second
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func (c *Client) flush(ctx context.Context) error {
	for len(c.State.Pending) > 0 {
		count := len(c.State.Pending)
		if count > 100 {
			count = 100
		}
		batch := struct {
			Results []protocol.JobResult `json:"results,omitempty"`
		}{Results: c.State.Pending[:count]}
		err := c.post(ctx, c.State.ServerURL, "/api/agent/reports", c.State.Credential, batch, nil)
		if err != nil {
			return fmt.Errorf("submit queued reports: %w", err)
		}
		c.State.Pending = append([]protocol.JobResult(nil), c.State.Pending[count:]...)
		if err := Save(c.StatePath, c.State); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) post(ctx context.Context, base, path, credential string, body, output any) error {
	if err := validateServerURL(base); err != nil {
		return err
	}
	endpoint, err := url.JoinPath(strings.TrimRight(base, "/"), path)
	if err != nil {
		return err
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if credential != "" {
		req.Header.Set("Authorization", "Bearer "+credential)
	}
	response, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("server returned HTTP %d", response.StatusCode)
	}
	if output != nil {
		decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
		if err := decoder.Decode(output); err != nil {
			return fmt.Errorf("decode server response: %w", err)
		}
	}
	return nil
}

func validateServerURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("server URL must be an origin without user info, query, or fragment")
	}
	if u.Scheme == "https" {
		return nil
	}
	host := u.Hostname()
	if u.Scheme == "http" && (host == "localhost" || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()) {
		return nil
	}
	return fmt.Errorf("server URL must use HTTPS")
}

func randomID() (string, error) {
	data := make([]byte, 16)
	if _, err := cryptorand.Read(data); err != nil {
		return "", err
	}
	data[6] = data[6]&0x0f | 0x40
	data[8] = data[8]&0x3f | 0x80
	encoded := base64.RawURLEncoding.EncodeToString(data)
	return encoded, nil
}
