package runtime

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"

	"github.com/deranjer/vigara/apps/agent/internal/collector"
	"github.com/deranjer/vigara/internal/modules"
	"github.com/deranjer/vigara/internal/protocol"
	"github.com/deranjer/vigara/internal/restic"
)

const AgentVersion = "0.1.0"
const MaxJobTimeout = 7 * 24 * time.Hour

type Client struct {
	StatePath string
	State     State
	HTTP      *http.Client
	Collector *collector.Collector
}

// TelemetryDeliveryError reports a rejected telemetry batch after Cycle has
// still completed the normal poll. The snapshots remain queued for retry.
type TelemetryDeliveryError struct{ Err error }

func (e *TelemetryDeliveryError) Error() string { return "submit queued telemetry: " + e.Err.Error() }
func (e *TelemetryDeliveryError) Unwrap() error { return e.Err }

func NewClient(statePath string) (*Client, error) {
	state, err := Load(statePath)
	if err != nil {
		return nil, err
	}
	return &Client{StatePath: statePath, State: state, HTTP: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}, Collector: collector.New()}, nil
}

func (c *Client) Enroll(ctx context.Context, serverURL, token, name string) error {
	if err := validateServerURL(serverURL); err != nil {
		return err
	}
	capability := protocol.CapabilityReport{ProtocolVersion: protocol.CurrentVersion, Platform: runtime.GOOS, Architecture: runtime.GOARCH, AgentVersion: AgentVersion, Capabilities: c.capabilities()}
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
	if err := c.collectTelemetry(ctx); err != nil {
		return 0, err
	}
	if err := c.flushReports(ctx); err != nil {
		return 0, err
	}
	telemetryErr := c.flushTelemetry(ctx)
	request := protocol.AgentRequest{ProtocolVersion: protocol.CurrentVersion, AgentID: c.State.AgentID, AgentVersion: AgentVersion, Platform: runtime.GOOS, Architecture: runtime.GOARCH, Capabilities: c.capabilities()}
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
		if err := Save(c.StatePath, c.State); err != nil {
			return 0, err
		}
	}
	if response.Job != nil {
		if len(c.State.Pending) >= MaxQueuedReports {
			return 0, fmt.Errorf("offline report queue is full (%d)", MaxQueuedReports)
		}
		resultID, err := randomID()
		if err != nil {
			return 0, err
		}
		result := c.executeJob(ctx, *response.Job, resultID)
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
	delay += time.Duration(rand.Int64N(int64(delay/10 + 1)))
	if telemetryErr != nil {
		return delay, &TelemetryDeliveryError{Err: telemetryErr}
	}
	return delay, nil
}

func (c *Client) capabilities() []string {
	if resticCapabilityEnabled(c.State.ResticProfiles) {
		return []string{resticCapability}
	}
	return []string{}
}

func (c *Client) executeJob(parent context.Context, job protocol.Job, resultID string) (result protocol.JobResult) {
	started := time.Now().UTC()
	result = protocol.JobResult{ProtocolVersion: protocol.CurrentVersion, ID: resultID, JobID: job.ID, AgentID: c.State.AgentID, ModuleID: job.ModuleID, ModuleVersion: job.ModuleVersion, Status: protocol.JobFailed, StartedAt: started}
	defer func() {
		if recovered := recover(); recovered != nil {
			result.Status = protocol.JobFailed
			result.Error = &protocol.Failure{Code: "module_panic", Message: "The requested module failed unexpectedly", Retryable: false}
		}
		result.FinishedAt = time.Now().UTC()
	}()
	if job.ID == "" || job.ModuleID != resticModuleID || job.ModuleVersion != resticModuleVersion || job.Kind != "backup" {
		result.Error = &protocol.Failure{Code: "unsupported_capability", Message: "This agent cannot execute the requested module job", Retryable: false}
		return result
	}
	if !moduleEnabled(c.State.Configuration, resticModuleID, resticModuleVersion) {
		result.Error = &protocol.Failure{Code: "module_disabled", Message: "The requested module is not enabled in local desired configuration", Retryable: false}
		return result
	}
	if job.TimeoutSeconds <= 0 || job.TimeoutSeconds > int(MaxJobTimeout/time.Second) {
		result.Error = &protocol.Failure{Code: "invalid_timeout", Message: "The job has no valid execution timeout", Retryable: false}
		return result
	}
	deadline := started.Add(time.Duration(job.TimeoutSeconds) * time.Second)
	if !job.Deadline.IsZero() && job.Deadline.Before(deadline) {
		deadline = job.Deadline
	}
	if !deadline.After(started) {
		result.Status = protocol.JobTimedOut
		result.Error = &protocol.Failure{Code: "deadline_exceeded", Message: "The job deadline has expired", Retryable: true}
		return result
	}
	ctx, cancel := context.WithDeadline(parent, deadline)
	defer cancel()
	configuration := make(map[string]any, len(job.Settings))
	for key, value := range job.Settings {
		configuration[key] = value
	}
	grants := make([]modules.PermissionGrant, 0, len(job.Permissions))
	for _, grant := range job.Permissions {
		grants = append(grants, modules.PermissionGrant{Name: grant.Name, Scope: grant.Scope})
	}
	module := resticBackupModule{profiles: c.State.ResticProfiles}
	outcome, err := module.Execute(ctx, modules.JobContext{JobID: job.ID, Configuration: configuration, Permissions: grants, StartedAt: started})
	if err != nil {
		code := err.Error()
		if !validProfileID(code) {
			code = "module_failed"
		}
		result.Error = &protocol.Failure{Code: code, Message: "The backup profile or its permission grant is invalid", Retryable: false}
		return result
	}
	result.Output = outcome.Output
	status, _ := outcome.Output["resticStatus"].(restic.Status)
	if ctx.Err() == context.DeadlineExceeded {
		result.Status = protocol.JobTimedOut
		result.Error = &protocol.Failure{Code: "deadline_exceeded", Message: "The backup job exceeded its deadline", Retryable: true}
		return result
	}
	if parent.Err() != nil || status == restic.Canceled {
		result.Status = protocol.JobCancelled
		result.Error = &protocol.Failure{Code: "canceled", Message: "The backup job was canceled", Retryable: true}
		return result
	}
	if status == restic.Succeeded {
		result.Status = protocol.JobSucceeded
		return result
	}
	// v1 has no partial/interrupted job state. Preserve the runner's exact
	// outcome in output and report both as failed at the protocol boundary.
	code, _ := outcome.Output["errorCode"].(string)
	if code == "" {
		code = string(status)
	}
	result.Error = &protocol.Failure{Code: code, Message: "The backup job did not complete successfully", Retryable: status == restic.Interrupted}
	return result
}

func moduleEnabled(configuration *protocol.AgentConfiguration, moduleID, version string) bool {
	if configuration == nil {
		return false
	}
	for _, module := range configuration.Modules {
		if module.ModuleID == moduleID && module.ModuleVersion == version {
			return module.Enabled
		}
	}
	return false
}

func (c *Client) Run(ctx context.Context) error {
	backoff := time.Second
	for {
		delay, err := c.Cycle(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var deliveryErr *TelemetryDeliveryError
			if errors.As(err, &deliveryErr) {
				slog.Warn("telemetry delivery failed; queued snapshots retained while polling continues", "error", deliveryErr.Err)
				backoff = time.Second
			} else {
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

func (c *Client) flushReports(ctx context.Context) error {
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

func (c *Client) flushTelemetry(ctx context.Context) error {
	for len(c.State.Telemetry) > 0 {
		count := len(c.State.Telemetry)
		if count > 5 {
			count = 5
		}
		batch := protocol.TelemetrySnapshotBatch{ProtocolVersion: protocol.CurrentVersion, AgentID: c.State.AgentID, Snapshots: c.State.Telemetry[:count]}
		if err := c.post(ctx, c.State.ServerURL, "/api/agent/telemetry", c.State.Credential, batch, nil); err != nil {
			return err
		}
		c.State.Telemetry = append([]protocol.TelemetrySnapshot(nil), c.State.Telemetry[count:]...)
		if err := Save(c.StatePath, c.State); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) collectTelemetry(ctx context.Context) error {
	if c.Collector == nil {
		c.Collector = collector.New()
	}
	if len(c.State.Telemetry) >= MaxQueuedTelemetry {
		return nil
	}
	collectionCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	snapshot := c.Collector.Collect(collectionCtx)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	id, err := randomID()
	if err != nil {
		return err
	}
	snapshot.ID, snapshot.AgentID = id, c.State.AgentID
	snapshot.Host.AgentVersion = AgentVersion
	if err := c.State.EnqueueTelemetry(snapshot); err != nil {
		return err
	}
	return Save(c.StatePath, c.State)
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
