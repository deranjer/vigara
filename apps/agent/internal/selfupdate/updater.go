// Package selfupdate implements a separately scheduled privileged updater. Its
// enrollment copy, journal, and staging files are outside agent-writable state.
package selfupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/deranjer/vigara/internal/protocol"
)

const maxPackage = 128 << 20

type enrollment struct {
	ServerURL  string `json:"serverUrl"`
	AgentID    string `json:"agentId"`
	Credential string `json:"credential"`
}

type journal struct {
	Plan    protocol.AgentUpdatePlan `json:"plan"`
	Phase   string                   `json:"phase"`
	Message string                   `json:"message,omitempty"`
}

// Configure is called by the elevated bundle installer, never the running
// unprivileged agent. Reinstallation refreshes rotated enrollment credentials.
func Configure(statePath string) error {
	if !privileged() {
		return errors.New("updater setup requires administrator access")
	}
	if err := os.MkdirAll(Directory, 0755); err != nil {
		return err
	}
	if err := os.Chmod(Directory, 0755); err != nil {
		return err
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		return err
	}
	var config enrollment
	if json.Unmarshal(data, &config) != nil || config.AgentID == "" || config.Credential == "" {
		return errors.New("saved enrollment is incomplete")
	}
	if err = validateOrigin(config.ServerURL); err != nil {
		// Never leave old-server credentials active after an HTTP re-enrollment.
		_ = os.Remove(filepath.Join(Directory, "enrollment.json"))
		fmt.Println("Client self updates are disabled: an HTTPS server address is required.")
		return nil
	}
	lock, err := os.OpenFile(filepath.Join(Directory, "operations.lock"), os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return err
	}
	if err = lock.Chmod(0644); err != nil {
		lock.Close()
		return err
	}
	lock.Close()
	lock, err = os.OpenFile(filepath.Join(Directory, "updater.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	lock.Close()
	if err = writeJSON(filepath.Join(Directory, "enrollment.json"), config); err != nil {
		return err
	}
	// A deliberate bundle reinstall supersedes any interrupted older attempt.
	if err = os.Remove(filepath.Join(Directory, "journal.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func validateOrigin(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("self updates require an enrolled HTTPS origin")
	}
	return nil
}

type runner struct {
	config            enrollment
	client            *http.Client
	directory, binary string
	service           func(string) error
	healthTimeout     time.Duration
}

// Run executes one bounded scheduled scan. The helper is a separate installed
// copy of the executable so stopping/replacing the agent cannot kill it.
func Run(ctx context.Context) error {
	if !privileged() {
		return errors.New("updater requires administrator access")
	}
	data, err := os.ReadFile(filepath.Join(Directory, "enrollment.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var config enrollment
	if json.Unmarshal(data, &config) != nil || config.Credential == "" || validateOrigin(config.ServerURL) != nil {
		return errors.New("invalid updater enrollment")
	}
	lockCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	release, err := acquire(lockCtx, filepath.Join(Directory, "updater.lock"), true, false)
	if err != nil {
		return err
	}
	defer release()
	r := runner{config: config, client: &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, directory: Directory, binary: Binary, service: service, healthTimeout: 2 * time.Minute}
	return r.run(ctx)
}

func (r *runner) request(ctx context.Context, method, path string, payload any) (*http.Response, error) {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(r.config.ServerURL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+r.config.Credential)
	req.Header.Set("Content-Type", "application/json")
	return r.client.Do(req)
}

func (r *runner) report(ctx context.Context, j journal) {
	response, err := r.request(ctx, "POST", "/api/agent/update-status", protocol.AgentUpdateStatus{State: j.Phase, Version: j.Plan.Version, SHA256: j.Plan.SHA256, Generation: j.Plan.Generation, Message: j.Message})
	if err == nil {
		response.Body.Close()
	}
}

func validPlan(p protocol.AgentUpdatePlan) bool {
	digest, err := hex.DecodeString(p.SHA256)
	return err == nil && len(digest) == sha256.Size && p.SHA256 == strings.ToLower(p.SHA256) && p.Version != "" && len(p.Version) <= 80 && p.Size > 0 && p.Size <= maxPackage && p.Generation >= 0
}

func (r *runner) run(ctx context.Context) error {
	path := filepath.Join(r.directory, "journal.json")
	var previous journal
	if data, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(data, &previous) != nil {
			return errors.New("invalid updater recovery journal")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if previous.Phase == "installing" || previous.Phase == "checking" {
		// A crash/reboot after mutation is recovered before checking new releases.
		if err := r.rollback(); err != nil {
			return err
		}
		previous.Phase = "rolled_back"
		previous.Message = "Recovered an interrupted installation; request Update now to retry."
		if err := writeJSON(path, previous); err != nil {
			return err
		}
		r.report(ctx, previous)
	}
	response, err := r.request(ctx, "GET", "/api/agent/update-plan", nil)
	if err != nil {
		return errors.New("could not reach enrolled update server")
	}
	defer response.Body.Close()
	if response.StatusCode == 204 {
		if previous.Phase == "" {
			r.report(ctx, journal{Phase: "idle"})
		} else {
			r.report(ctx, previous)
		}
		return nil
	}
	if response.StatusCode != 200 {
		return fmt.Errorf("update plan returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	var plan protocol.AgentUpdatePlan
	if err != nil || len(data) > 4096 || json.Unmarshal(data, &plan) != nil || !validPlan(plan) {
		return errors.New("invalid update plan")
	}
	if previous.Plan.SHA256 == plan.SHA256 && previous.Plan.Generation == plan.Generation && (previous.Phase == "failed" || previous.Phase == "rolled_back") {
		r.report(ctx, previous)
		return nil
	}
	current, err := fileDigest(r.binary)
	if err != nil {
		return err
	}
	if current == plan.SHA256 {
		r.report(ctx, journal{Plan: plan, Phase: "updated"})
		return nil
	}
	j := journal{Plan: plan, Phase: "downloading"}
	r.report(ctx, j)
	stage := r.binary + ".update-new"
	if err = r.download(ctx, plan, stage); err != nil {
		j.Phase = "failed"
		j.Message = "Package download or checksum verification failed; the client will retry on its next check."
		r.report(ctx, j)
		return err
	}
	defer os.Remove(stage)
	j.Phase = "waiting"
	r.report(ctx, j)
	lockCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	release, err := acquire(lockCtx, filepath.Join(r.directory, "operations.lock"), true, false)
	if err != nil {
		return nil
	} // Active work is left alone; the next scan retries.
	locked := true
	defer func() {
		if locked {
			release()
		}
	}()
	// Capture the server's last poll time after draining work. This avoids clock
	// skew and requires a genuinely new check-in even for same-version rebuilds.
	started, err := r.healthBaseline(ctx)
	if err != nil {
		return err
	}
	// Save a known-good executable before recording any mutating phase.
	if err = copyFile(r.binary, r.binary+".update-old"); err != nil {
		return err
	}
	j.Phase = "installing"
	if err = writeJSON(path, j); err != nil {
		return err
	}
	r.report(ctx, j)
	if err = r.service("stop"); err != nil {
		// Never replace an executable unless the service is confirmed stopped.
		// A timed-out stop can still complete later. Keep recovery pending so a
		// subsequent scan restores/starts the known-good service instead of leaving
		// it stopped indefinitely or replacing a process still shutting down.
		j.Message = "Service stop was not confirmed; executable preserved and recovery will retry."
		_ = writeJSON(path, j)
		r.report(ctx, j)
		return err
	}
	err = os.Rename(stage, r.binary)
	if err == nil {
		err = syncParent(r.binary)
	}
	if err == nil {
		j.Phase = "checking"
		err = writeJSON(path, j)
	}
	if err == nil {
		err = r.service("start")
	}
	release()
	locked = false
	if err == nil {
		r.report(ctx, j)
		err = r.waitHealthy(ctx, plan.Version, started)
	}
	if err != nil {
		if recoveryErr := r.rollback(); recoveryErr != nil {
			return fmt.Errorf("update failed; recovery pending: %w", recoveryErr)
		}
		j.Phase = "rolled_back"
		j.Message = "Updated agent did not check in; restored the previous executable. Request Update now to retry."
	} else {
		j.Phase = "updated"
		j.Message = "Agent checked in successfully after installation."
	}
	if saveErr := writeJSON(path, j); saveErr != nil {
		return saveErr
	}
	r.report(ctx, j)
	return err
}

func (r *runner) download(ctx context.Context, p protocol.AgentUpdatePlan, path string) error {
	response, err := r.request(ctx, "GET", "/api/agent/update-package/"+p.SHA256, nil)
	if err != nil {
		return errors.New("package request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("package request returned HTTP %d", response.StatusCode)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0755)
	if err != nil {
		return err
	}
	if err = f.Chmod(0755); err != nil {
		f.Close()
		return err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(response.Body, p.Size+1))
	syncErr := f.Sync()
	closeErr := f.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil || n != p.Size || hex.EncodeToString(h.Sum(nil)) != p.SHA256 {
		os.Remove(path)
		return errors.New("package integrity check failed")
	}
	return syncParent(path)
}

func (r *runner) waitHealthy(ctx context.Context, version string, since time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, r.healthTimeout)
	defer cancel()
	for {
		response, err := r.request(ctx, "GET", "/api/agent/update-health?"+url.Values{"version": {version}, "since": {since.Format(time.RFC3339Nano)}}.Encode(), nil)
		if err == nil {
			var health struct {
				Healthy bool `json:"healthy"`
			}
			err = json.NewDecoder(io.LimitReader(response.Body, 1024)).Decode(&health)
			response.Body.Close()
			if response.StatusCode == 200 && err == nil && health.Healthy {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return errors.New("updated agent health check timed out")
		case <-time.After(2 * time.Second):
		}
	}
}

func (r *runner) healthBaseline(ctx context.Context) (time.Time, error) {
	response, err := r.request(ctx, "GET", "/api/agent/update-health", nil)
	if err != nil {
		return time.Time{}, errors.New("could not read agent health baseline")
	}
	defer response.Body.Close()
	var result struct {
		LastSeen time.Time `json:"lastSeenAt"`
	}
	if response.StatusCode != 200 || json.NewDecoder(io.LimitReader(response.Body, 1024)).Decode(&result) != nil || result.LastSeen.IsZero() {
		return time.Time{}, errors.New("invalid agent health baseline")
	}
	return result.LastSeen, nil
}

func (r *runner) rollback() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	release, err := acquire(ctx, filepath.Join(r.directory, "operations.lock"), true, false)
	if err != nil {
		return errors.New("recovery is waiting for active agent work")
	}
	defer release()
	if err := r.service("stop"); err != nil {
		return err
	}
	// Copy through a fresh file, retaining the recovery copy across further crashes.
	if err := copyFile(r.binary+".update-old", r.binary+".update-restore"); err != nil {
		return err
	}
	if err := os.Rename(r.binary+".update-restore", r.binary); err != nil {
		return err
	}
	if err := syncParent(r.binary); err != nil {
		return err
	}
	return r.service("start")
}

func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	_, err = io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), err
}
func copyFile(source, dest string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0755)
	if err != nil {
		return err
	}
	if err = out.Chmod(0755); err != nil {
		out.Close()
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	closeErr := out.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = syncParent(dest)
	}
	return err
}
func writeJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".update-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncParent(path)
}
