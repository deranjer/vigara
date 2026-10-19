package dockerapi

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/deranjer/vigara/internal/protocol"
)

const (
	actionTimeout   = 90 * time.Second
	swapTimeout     = 3 * time.Minute
	createTimeout   = 60 * time.Second
	layerEventEvery = 250 * time.Millisecond
)

// verifyDelay is how long a replacement must stay up before the original is removed.
var verifyDelay = 2 * time.Second

// RecreateError reports a failed recreate. RolledBack says whether the original
// container was put back; when it is false the original was never touched.
type RecreateError struct {
	Step       string
	Err        error
	RolledBack bool
}

func (e *RecreateError) Error() string {
	state := "the original container was not changed"
	if e.RolledBack {
		state = "the original container was restored"
	}
	return fmt.Sprintf("recreate failed during %s (%s): %v", e.Step, state, e.Err)
}

func (e *RecreateError) Unwrap() error { return e.Err }

func containerPath(id, suffix string) string { return "/containers/" + url.PathEscape(id) + suffix }

// post issues a state-changing request that returns no body we need.
func (c *Client) post(ctx context.Context, path string, query url.Values) (changed bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, actionTimeout)
	defer cancel()
	resp, err := c.do(ctx, http.MethodPost, path, query, nil)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode != http.StatusNotModified, nil
}

// Action performs one lifecycle action on one container.
func (c *Client) Action(ctx context.Context, p protocol.DockerActionParams) (protocol.DockerActionResult, error) {
	if err := checkRef(p.ID); err != nil {
		return protocol.DockerActionResult{}, err
	}
	if !slices.Contains(protocol.DockerActions, p.Action) {
		return protocol.DockerActionResult{}, ErrBadRequest
	}
	timeout := p.TimeoutSeconds
	if timeout <= 0 {
		timeout = protocol.DefaultStopSeconds
	}
	if timeout > protocol.MaxStopSeconds {
		timeout = protocol.MaxStopSeconds
	}
	signal := strings.ToUpper(strings.TrimPrefix(strings.ToUpper(p.Signal), "SIG"))
	if p.Action == protocol.ActionKill {
		if signal == "" {
			signal = "KILL"
		}
		if !slices.Contains(protocol.DockerSignals, signal) {
			return protocol.DockerActionResult{}, ErrBadRequest
		}
	}

	// Look the container up first so the result and audit record can name it.
	var meta struct {
		ID    string `json:"Id"`
		Name  string `json:"Name"`
		State struct{ Running bool }
	}
	if err := c.getJSON(ctx, containerPath(p.ID, "/json"), nil, maxInspectBytes, &meta); err != nil {
		return protocol.DockerActionResult{}, err
	}
	result := protocol.DockerActionResult{ID: meta.ID, Name: strings.TrimPrefix(meta.Name, "/"), Action: p.Action, Changed: true}

	var err error
	switch p.Action {
	case protocol.ActionStart:
		result.Changed, err = c.post(ctx, containerPath(p.ID, "/start"), nil)
		if !result.Changed {
			result.Message = "The container was already running."
		}
	case protocol.ActionStop:
		// Docker waits up to the timeout, so allow for that on top of the request budget.
		stopCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second+actionTimeout)
		defer cancel()
		result.Changed, err = c.post(stopCtx, containerPath(p.ID, "/stop"), url.Values{"t": {strconv.Itoa(timeout)}})
		if !result.Changed {
			result.Message = "The container was already stopped."
		}
	case protocol.ActionRestart:
		restartCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second+actionTimeout)
		defer cancel()
		_, err = c.post(restartCtx, containerPath(p.ID, "/restart"), url.Values{"t": {strconv.Itoa(timeout)}})
	case protocol.ActionPause:
		_, err = c.post(ctx, containerPath(p.ID, "/pause"), nil)
	case protocol.ActionUnpause:
		_, err = c.post(ctx, containerPath(p.ID, "/unpause"), nil)
	case protocol.ActionKill:
		_, err = c.post(ctx, containerPath(p.ID, "/kill"), url.Values{"signal": {signal}})
	case protocol.ActionRemove:
		query := url.Values{}
		if p.Force {
			query.Set("force", "1")
		}
		if p.RemoveVolumes {
			query.Set("v", "1")
		}
		reqCtx, cancel := context.WithTimeout(ctx, actionTimeout)
		defer cancel()
		var resp *http.Response
		if resp, err = c.do(reqCtx, http.MethodDelete, containerPath(p.ID, ""), query, nil); err == nil {
			resp.Body.Close()
		}
	}
	return result, err
}

var (
	imageIDPattern = regexp.MustCompile(`^(sha256:)?[a-f0-9]{12,64}$`)
	// A container whose reference has this form is pinned to exact content.
	digestRef = "@sha256:"
)

// recreateState tracks what has changed so a failure can be undone.
type recreateState struct {
	oldID, oldName, name string
	wasRunning           bool
	newID, newName       string
	oldRenamed           bool
	newRenamedToName     bool
}

func randomSuffix() string {
	buf := make([]byte, 4)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// Recreate replaces a container with a fresh copy built from its own
// configuration, optionally pulling its image first. It creates the new container
// before touching the old one, keeps the old one until the new one is verified
// running, and restores the old one if anything fails after the swap begins.
// Cancelling ctx stops the work before the swap; once the swap begins it always
// runs to completion or rollback.
func (c *Client) Recreate(ctx context.Context, p protocol.DockerRecreateParams, emit func(protocol.DockerRecreateEvent)) (protocol.DockerRecreateResult, error) {
	if err := checkRef(p.ID); err != nil {
		return protocol.DockerRecreateResult{}, err
	}
	if emit == nil {
		emit = func(protocol.DockerRecreateEvent) {}
	}

	var old map[string]any
	if err := c.getJSON(ctx, containerPath(p.ID, "/json"), nil, maxInspectBytes, &old); err != nil {
		return protocol.DockerRecreateResult{}, err
	}
	st := &recreateState{oldID: str(old["Id"]), name: strings.TrimPrefix(str(old["Name"]), "/")}
	if st.oldID == "" || st.name == "" {
		return protocol.DockerRecreateResult{}, errors.New("docker returned an unexpected container description")
	}
	st.wasRunning = boolean(dig(old, "State", "Running"))
	oldImageID := str(old["Image"])
	config, _ := old["Config"].(map[string]any)
	hostConfig, _ := old["HostConfig"].(map[string]any)
	if config == nil || hostConfig == nil {
		return protocol.DockerRecreateResult{}, errors.New("docker returned an unexpected container description")
	}
	ref := str(config["Image"])
	if ref == "" {
		return protocol.DockerRecreateResult{}, errors.New("the container has no image reference")
	}

	if p.Pull {
		if strings.Contains(ref, digestRef) || imageIDPattern.MatchString(ref) {
			emit(protocol.DockerRecreateEvent{Step: protocol.StepSkipped, Message: "The image is pinned to exact content, so there is nothing newer to pull."})
		} else if err := c.pull(ctx, ref, emit); err != nil {
			return protocol.DockerRecreateResult{}, &RecreateError{Step: protocol.StepPull, Err: err}
		}
	}
	if err := ctx.Err(); err != nil {
		return protocol.DockerRecreateResult{}, err
	}

	// Build and create the replacement while the original keeps running.
	body, err := createBody(config, hostConfig, old)
	if err != nil {
		return protocol.DockerRecreateResult{}, &RecreateError{Step: protocol.StepCreate, Err: err}
	}
	st.newName = st.name + "-vigara-new-" + randomSuffix()
	emit(protocol.DockerRecreateEvent{Step: protocol.StepCreate, Message: "Creating the replacement container."})
	createCtx, cancelCreate := context.WithTimeout(ctx, createTimeout)
	resp, err := c.do(createCtx, http.MethodPost, "/containers/create", url.Values{"name": {st.newName}}, body)
	if err != nil {
		cancelCreate()
		return protocol.DockerRecreateResult{}, &RecreateError{Step: protocol.StepCreate, Err: err}
	}
	var created struct {
		ID string `json:"Id"`
	}
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&created)
	resp.Body.Close()
	cancelCreate()
	if decodeErr != nil || created.ID == "" {
		return protocol.DockerRecreateResult{}, &RecreateError{Step: protocol.StepCreate, Err: errors.New("docker did not return the new container's ID")}
	}
	st.newID = created.ID
	removeNew := func(parent context.Context) {
		cleanCtx, cancel := context.WithTimeout(context.WithoutCancel(parent), actionTimeout)
		defer cancel()
		if r, err := c.do(cleanCtx, http.MethodDelete, containerPath(st.newID, ""), url.Values{"force": {"1"}}, nil); err == nil {
			r.Body.Close()
		}
	}
	if err := ctx.Err(); err != nil {
		removeNew(ctx)
		return protocol.DockerRecreateResult{}, err
	}
	var fresh struct {
		Image string `json:"Image"`
	}
	if err := c.getJSON(ctx, containerPath(st.newID, "/json"), nil, maxInspectBytes, &fresh); err != nil {
		removeNew(ctx)
		return protocol.DockerRecreateResult{}, &RecreateError{Step: protocol.StepCreate, Err: err}
	}

	// From here on the swap must finish or roll back, even if the caller goes away.
	swapCtx, cancelSwap := context.WithTimeout(context.WithoutCancel(ctx), swapTimeout)
	defer cancelSwap()
	failStep, swapErr := c.swap(swapCtx, st, emit)
	if swapErr != nil {
		emit(protocol.DockerRecreateEvent{Step: protocol.StepRollback, Message: "Restoring the original container."})
		// The swap may have failed because its own deadline ran out, so rollback gets a fresh one.
		rollbackCtx, cancelRollback := context.WithTimeout(context.WithoutCancel(ctx), swapTimeout)
		rollbackErr := c.rollback(rollbackCtx, st)
		cancelRollback()
		if rollbackErr != nil {
			return protocol.DockerRecreateResult{}, &RecreateError{Step: failStep, Err: fmt.Errorf("%w; restoring the original also failed: %v", swapErr, rollbackErr)}
		}
		return protocol.DockerRecreateResult{ID: st.oldID, Name: st.name, PreviousImageID: oldImageID, ImageID: oldImageID, RolledBack: true},
			&RecreateError{Step: failStep, Err: swapErr, RolledBack: true}
	}

	result := protocol.DockerRecreateResult{ID: st.newID, Name: st.name, PreviousImageID: oldImageID, ImageID: fresh.Image, ImageChanged: oldImageID != fresh.Image, Started: st.wasRunning}
	emit(protocol.DockerRecreateEvent{Step: protocol.StepCleanup, Message: "Removing the original container."})
	cleanCtx, cancelClean := context.WithTimeout(swapCtx, actionTimeout)
	defer cancelClean()
	if r, err := c.do(cleanCtx, http.MethodDelete, containerPath(st.oldID, ""), nil, nil); err != nil {
		emit(protocol.DockerRecreateEvent{Step: protocol.StepSkipped, Message: "The replacement is running, but the original could not be removed and was left as " + st.name + "-vigara-old-*."})
	} else {
		r.Body.Close()
	}
	emit(protocol.DockerRecreateEvent{Step: protocol.StepDone, Result: &result})
	return result, nil
}

// swap stops the original, moves the names, starts the replacement, and checks it.
// It returns the step that failed so the caller can report and roll back.
func (c *Client) swap(ctx context.Context, st *recreateState, emit func(protocol.DockerRecreateEvent)) (string, error) {
	if st.wasRunning {
		emit(protocol.DockerRecreateEvent{Step: protocol.StepStopOld, Message: "Stopping the original container."})
		if _, err := c.post(ctx, containerPath(st.oldID, "/stop"), url.Values{"t": {strconv.Itoa(protocol.DefaultStopSeconds)}}); err != nil {
			return protocol.StepStopOld, err
		}
	}
	emit(protocol.DockerRecreateEvent{Step: protocol.StepSwap, Message: "Swapping names."})
	oldName := st.name + "-vigara-old-" + strconv.FormatInt(time.Now().Unix(), 10)
	if _, err := c.post(ctx, containerPath(st.oldID, "/rename"), url.Values{"name": {oldName}}); err != nil {
		return protocol.StepSwap, err
	}
	st.oldRenamed = true
	if _, err := c.post(ctx, containerPath(st.newID, "/rename"), url.Values{"name": {st.name}}); err != nil {
		return protocol.StepSwap, err
	}
	st.newRenamedToName = true
	if !st.wasRunning {
		return "", nil
	}
	emit(protocol.DockerRecreateEvent{Step: protocol.StepStart, Message: "Starting the replacement."})
	if _, err := c.post(ctx, containerPath(st.newID, "/start"), nil); err != nil {
		return protocol.StepStart, err
	}
	emit(protocol.DockerRecreateEvent{Step: protocol.StepVerify, Message: "Checking that the replacement stays up."})
	select {
	case <-time.After(verifyDelay):
	case <-ctx.Done():
		return protocol.StepVerify, ctx.Err()
	}
	var state struct {
		State struct {
			Running    bool
			Restarting bool
			Status     string
		}
	}
	if err := c.getJSON(ctx, containerPath(st.newID, "/json"), nil, maxInspectBytes, &state); err != nil {
		return protocol.StepVerify, err
	}
	if !state.State.Running || state.State.Restarting {
		return protocol.StepVerify, fmt.Errorf("the replacement is %s", state.State.Status)
	}
	return "", nil
}

// rollback restores the original container as far as the swap had progressed.
func (c *Client) rollback(ctx context.Context, st *recreateState) error {
	var failures []string
	record := func(step string, err error) {
		if err != nil {
			failures = append(failures, step+": "+err.Error())
		}
	}
	// Remove the replacement first so its name and ports are free again.
	if r, err := c.do(ctx, http.MethodDelete, containerPath(st.newID, ""), url.Values{"force": {"1"}}, nil); err != nil && !errors.Is(err, ErrNotFound) {
		record("remove the replacement", err)
	} else if err == nil {
		r.Body.Close()
	}
	if st.oldRenamed {
		_, err := c.post(ctx, containerPath(st.oldID, "/rename"), url.Values{"name": {st.name}})
		record("restore the original name", err)
	}
	if st.wasRunning {
		_, err := c.post(ctx, containerPath(st.oldID, "/start"), nil)
		record("restart the original", err)
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

// pull fetches an image, forwarding throttled progress. Docker reports pull
// failures as an error record inside a successful stream, so those are checked.
func (c *Client) pull(ctx context.Context, ref string, emit func(protocol.DockerRecreateEvent)) error {
	emit(protocol.DockerRecreateEvent{Step: protocol.StepPull, Message: "Pulling " + ref + "."})
	resp, err := c.do(ctx, http.MethodPost, "/images/create", url.Values{"fromImage": {ref}}, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	lastLayerEvent := time.Time{}
	for scanner.Scan() {
		var line struct {
			Status         string `json:"status"`
			ID             string `json:"id"`
			Error          string `json:"error"`
			ProgressDetail struct {
				Current int64 `json:"current"`
				Total   int64 `json:"total"`
			} `json:"progressDetail"`
		}
		if json.Unmarshal(scanner.Bytes(), &line) != nil {
			continue
		}
		if line.Error != "" {
			message := line.Error
			if len(message) > 300 {
				message = message[:300]
			}
			return errors.New(message)
		}
		switch {
		case line.ID != "" && (line.Status == "Downloading" || line.Status == "Extracting"):
			if time.Since(lastLayerEvent) >= layerEventEvery {
				lastLayerEvent = time.Now()
				emit(protocol.DockerRecreateEvent{Step: protocol.StepPullLayer, Layer: line.ID, Message: line.Status, Current: line.ProgressDetail.Current, Total: line.ProgressDetail.Total})
			}
		case strings.HasPrefix(line.Status, "Status:"), strings.HasPrefix(line.Status, "Pulling from"):
			emit(protocol.DockerRecreateEvent{Step: protocol.StepPull, Message: line.Status})
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		return err
	}
	return ctx.Err()
}

// createBody builds the create request from an existing container's own
// description so the copy keeps its image, environment, mounts, limits, labels, and
// networks. Fields Docker assigns per container (hostname, addresses, IDs) are dropped.
func createBody(config, hostConfig map[string]any, old map[string]any) ([]byte, error) {
	body := make(map[string]any, len(config)+2)
	for key, value := range config {
		body[key] = value
	}
	id := str(old["Id"])
	if hostname := str(config["Hostname"]); hostname != "" && len(id) >= len(hostname) && strings.HasPrefix(id, hostname) {
		delete(body, "Hostname") // Docker generated it from the old ID; let it generate a new one.
	}
	body["HostConfig"] = hostConfig
	mode := str(hostConfig["NetworkMode"])
	if mode != "host" && mode != "none" && !strings.HasPrefix(mode, "container:") {
		endpoints := map[string]any{}
		networks, _ := dig(old, "NetworkSettings", "Networks").(map[string]any)
		for name, raw := range networks {
			attachment, _ := raw.(map[string]any)
			endpoint := map[string]any{}
			if value := attachment["IPAMConfig"]; value != nil {
				endpoint["IPAMConfig"] = value
			}
			if value := attachment["Links"]; value != nil {
				endpoint["Links"] = value
			}
			if value := attachment["DriverOpts"]; value != nil {
				endpoint["DriverOpts"] = value
			}
			if aliases, ok := attachment["Aliases"].([]any); ok {
				kept := []any{}
				for _, alias := range aliases {
					if text := str(alias); text != "" && !(len(text) >= 12 && strings.HasPrefix(id, text)) {
						kept = append(kept, text)
					}
				}
				if len(kept) > 0 {
					endpoint["Aliases"] = kept
				}
			}
			endpoints[name] = endpoint
		}
		if len(endpoints) > 0 {
			body["NetworkingConfig"] = map[string]any{"EndpointsConfig": endpoints}
		}
	}
	return json.Marshal(body)
}

func str(value any) string {
	text, _ := value.(string)
	return text
}

func boolean(value any) bool {
	flag, _ := value.(bool)
	return flag
}

// dig walks nested JSON objects.
func dig(root map[string]any, path ...string) any {
	var current any = root
	for _, key := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = object[key]
	}
	return current
}
