// Package composedeploy runs Compose only in locally approved project directories.
package composedeploy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/deranjer/vigara/apps/agent/internal/channel"
	"github.com/deranjer/vigara/internal/composesecrets"
	"github.com/deranjer/vigara/internal/placement"
	"github.com/deranjer/vigara/internal/protocol"
	"github.com/deranjer/vigara/internal/stack"
	"gopkg.in/yaml.v3"
)

type Target struct {
	ProjectName string `json:"projectName"`
	Directory   string `json:"directory"`
}
type Plan = stack.Plan
type Request = stack.DeployRequest
type Runner struct {
	ManagedRoot                string
	Targets                    map[string]Target
	DockerHost, Identity, SOPS string
	mu                         sync.Mutex
	runCommand                 func(context.Context, string, ...string) ([]byte, error)
}

var errTarget = errors.New("stack target unavailable, changed, or not approved on this device")
var errCompose = errors.New("Compose validation or deployment failed; check the stack configuration and device prerequisites")
var errJournal = errors.New("the deployment journal could not confirm the outcome; inspect this device before retrying")
var errExternalNetwork = errors.New("a required external network is unavailable")
var errExternalVolume = errors.New("a required external volume is unavailable")
var errBindMount = errors.New("a required bind mount is missing on this device")
var errUnsupportedStack = errors.New("this runner supports image-based stacks with interpolation inputs; builds, hooks and service file inputs need migration first")
var immutableImage = regexp.MustCompile(`^[^\s@]+@sha256:[a-f0-9]{64}$`)

// Only known, fixed messages may cross the channel. Command output and wrapped
// errors may include rendered environment values or host credentials.
func validationFailure(err error) error {
	for _, known := range []error{errTarget, errCompose, errJournal, errExternalNetwork, errExternalVolume, errBindMount, errUnsupportedStack, stack.ErrInvalid} {
		if errors.Is(err, known) {
			return &protocol.ChannelError{Code: protocol.ErrCodeBadRequest, Message: known.Error()}
		}
	}
	return err
}

func validImages(images map[string]string) bool {
	if len(images) == 0 || len(images) > 256 {
		return false
	}
	for name, image := range images {
		if name == "" || !immutableImage.MatchString(image) {
			return false
		}
	}
	return true
}

// releaseToken binds the reviewed immutable image set to the snapshot and device files.
func releaseToken(t Target, s stack.Snapshot, images map[string]string) (string, error) {
	files, err := fingerprint(t, s)
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(images)
	sum := sha256.Sum256(append([]byte("VIGARA-RELEASE-V1\x00"+s.ID+"\x00"+s.Commit+"\x00"+files+"\x00"), b...))
	return hex.EncodeToString(sum[:]), nil
}

// imageOverride contains no rendered environment values and is safe to retain with the release journal.
func imageOverride(file string, images map[string]string) error {
	if !validImages(images) {
		return stack.ErrInvalid
	}
	services := map[string]any{}
	for name, image := range images {
		services[name] = map[string]string{"image": image}
	}
	b, err := json.Marshal(map[string]any{"services": services})
	if err != nil {
		return errJournal
	}
	// Each release owns a new file. Exclusive creation also refuses a symlink
	// or an orphaned override rather than overwriting an uncertain release.
	f, err := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return errJournal
	}
	_, err = f.Write(b)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		os.Remove(file)
		return errJournal
	}
	return nil
}

func Register(c *channel.Client, r *Runner) {
	c.Calls[protocol.MethodComposeImagePlan] = func(ctx context.Context, p json.RawMessage) (any, error) {
		var q struct {
			Snapshot stack.Snapshot    `json:"snapshot"`
			Images   map[string]string `json:"images"`
		}
		if json.Unmarshal(p, &q) != nil || !validImages(q.Images) {
			return nil, stack.ErrInvalid
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		t, m, err := r.target(q.Snapshot)
		if err != nil {
			return nil, validationFailure(err)
		}
		plan, err := r.modelWithImages(ctx, q.Snapshot, t, m, q.Images)
		return plan, validationFailure(err)
	}
	c.Calls[protocol.MethodComposeTargets] = func(ctx context.Context, p json.RawMessage) (any, error) { return r.TargetInfo(), nil }
	c.Calls[protocol.MethodComposeCreate] = func(ctx context.Context, p json.RawMessage) (any, error) {
		var q protocol.ComposeCreateTarget
		if json.Unmarshal(p, &q) != nil {
			return nil, stack.ErrInvalid
		}
		return r.CreateTarget(ctx, q)
	}

	c.Calls[protocol.MethodComposeValidate] = func(ctx context.Context, p json.RawMessage) (any, error) {
		var s stack.Snapshot
		if json.Unmarshal(p, &s) != nil {
			return nil, stack.ErrInvalid
		}
		plan, err := r.Plan(ctx, s)
		return plan, validationFailure(err)
	}
	c.Calls[protocol.MethodComposeDeploy] = func(ctx context.Context, p json.RawMessage) (any, error) {
		var q Request
		if json.Unmarshal(p, &q) != nil {
			return nil, stack.ErrInvalid
		}
		result, err := r.Deploy(ctx, q)
		if err != nil {
			status := "failed"
			if errors.Is(err, errJournal) {
				status = "unknown"
			}
			return stack.Result{Status: status, Stage: "validate", Message: err.Error(), JobID: q.Job.ID, Commit: q.Job.Snapshot.Commit, ConfigurationRevision: stack.ConfigurationRevision(q.Job.Snapshot), PlanToken: q.Token, Images: q.Images, Readiness: "not-checked"}, nil
		}
		return result, nil
	}
	c.Calls[protocol.MethodComposeStatus] = func(ctx context.Context, p json.RawMessage) (any, error) {
		var q struct {
			StackID string `json:"stackId"`
			JobID   string `json:"jobId"`
		}
		if json.Unmarshal(p, &q) != nil || !stack.ValidID(q.StackID) || !stack.CommitPattern.MatchString(q.JobID) {
			return nil, stack.ErrInvalid
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		t, ok := r.lookup(q.StackID)
		if !ok {
			return nil, errTarget
		}
		var result stack.Result
		b, e := os.ReadFile(filepath.Join(t.Directory, ".vigara-jobs", q.JobID+".json"))
		if e != nil || json.Unmarshal(b, &result) != nil {
			return stack.Result{Status: "unknown", Stage: "reconcile", Message: "No final result recorded. Inspect the device before another deployment."}, nil
		}
		return result, nil
	}
}

func (r *Runner) target(s stack.Snapshot) (Target, stack.Manifest, error) {
	_, _, m, e := placement.ResolveRuntime(s)
	if e != nil {
		return Target{}, m, e
	}
	t, ok := r.lookup(s.ID)
	if !ok || t.ProjectName != m.ProjectName || !filepath.IsAbs(t.Directory) || runtime.GOOS == "windows" {
		return t, m, errTarget
	}
	actual, e := filepath.EvalSymlinks(t.Directory)
	if e != nil || filepath.Clean(actual) != filepath.Clean(t.Directory) {
		return t, m, errTarget
	}
	info, e := os.Stat(t.Directory)
	if e != nil || !info.IsDir() {
		return t, m, errTarget
	}
	return t, m, nil
}
func safeFile(root, p string) error {
	if !stack.ValidPath(p) {
		return stack.ErrInvalid
	}
	current := root
	for _, part := range strings.Split(p, "/") {
		current = filepath.Join(current, part)
		info, e := os.Lstat(current)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil || info.Mode()&os.ModeSymlink != 0 {
			return errTarget
		}
	}
	return nil
}
func fingerprint(t Target, s stack.Snapshot) (string, error) {
	h := sha256.New()
	h.Write([]byte(stack.Digest(s) + "\x00" + t.Directory + "\x00" + t.ProjectName))
	keys := []string{}
	for p := range s.Files {
		keys = append(keys, p)
	}
	sort.Strings(keys)
	for _, p := range keys {
		if e := safeFile(t.Directory, p); e != nil {
			return "", e
		}
		full := filepath.Join(t.Directory, filepath.FromSlash(p))
		info, e := os.Lstat(full)
		if os.IsNotExist(e) {
			h.Write([]byte(p + "\x00missing\x00"))
			continue
		}
		if e != nil || !info.Mode().IsRegular() || info.Size() > 128<<10 {
			return "", errTarget
		}
		b, e := os.ReadFile(full)
		if e != nil {
			return "", errTarget
		}
		sum := sha256.Sum256(b)
		h.Write([]byte(p + "\x00" + hex.EncodeToString(sum[:]) + "\x00"))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func (r *Runner) command(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if r.runCommand != nil {
		return r.runCommand(ctx, dir, args...)
	}
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = dir
	// Do not interpolate server or agent service secrets into Compose.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "DOCKER_HOST=" + r.DockerHost, "COMPOSE_DISABLE_ENV_FILE=1", "COMPOSE_ANSI=never", "COMPOSE_PROGRESS=plain"}
	out := &limitWriter{limit: 2 << 20}
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	if cmd.Run() != nil {
		return nil, errCompose
	}
	return out.Bytes(), nil
}

type limitWriter struct {
	bytes.Buffer
	limit int
}

func (w *limitWriter) Write(p []byte) (int, error) {
	if w.Len()+len(p) > w.limit {
		return 0, errCompose
	}
	return w.Buffer.Write(p)
}

func (r *Runner) workspace(ctx context.Context, s stack.Snapshot, m stack.Manifest) (string, []string, error) {
	dir, e := os.MkdirTemp("", "vigara-compose-")
	if e != nil {
		return "", nil, errTarget
	}
	fail := func(e error) (string, []string, error) { os.RemoveAll(dir); return "", nil, e }
	for p, v := range s.Files {
		dest := filepath.Join(dir, filepath.FromSlash(p))
		if os.MkdirAll(filepath.Dir(dest), 0700) != nil || os.WriteFile(dest, []byte(v), 0600) != nil {
			return fail(errTarget)
		}
	}
	args := []string{"compose", "-p", m.ProjectName, "--project-directory", dir}
	for _, p := range m.Compose.Files {
		args = append(args, "-f", filepath.Join(dir, filepath.FromSlash(p)))
	}
	for i, input := range m.Compose.InterpolationFiles {
		plain, e := composesecrets.DecryptDotenv(ctx, r.SOPS, r.Identity, []byte(s.Files[input.Path]))
		if e != nil {
			return fail(errors.New("encrypted environment could not be decrypted on this device"))
		}
		env := filepath.Join(dir, fmt.Sprintf(".vigara-input-%d", i))
		if os.WriteFile(env, plain, 0600) != nil {
			return fail(errTarget)
		}
		args = append(args, "--env-file", env)
	}
	return dir, args, nil
}

// model checks storage before applying. Relative binds are resolved against the original approved directory.
// Builds, includes, hooks, service env files and file-based configs/secrets are excluded in this first runner.
func (r *Runner) model(ctx context.Context, s stack.Snapshot, t Target, m stack.Manifest) (Plan, error) {
	return r.modelWithImages(ctx, s, t, m, nil)
}

func (r *Runner) modelWithImages(ctx context.Context, s stack.Snapshot, t Target, m stack.Manifest, images map[string]string) (Plan, error) {
	plan := Plan{ProjectName: m.ProjectName, Services: []string{}}
	p, _, _, e := placement.ResolveRuntime(s)
	if e != nil {
		return plan, e
	}
	if e = r.preflight(ctx, t.Directory, p); e != nil {
		return plan, e
	}
	for _, p := range m.Compose.Files {
		var raw map[string]any
		if yaml.Unmarshal([]byte(s.Files[p]), &raw) != nil {
			return plan, stack.ErrInvalid
		}
		for _, key := range []string{"include", "configs", "secrets"} {
			if _, ok := raw[key]; ok {
				return plan, stack.ErrInvalid
			}
		}
		services, ok := raw["services"].(map[string]any)
		if !ok {
			if raw["services"] == nil {
				continue
			}
			return plan, stack.ErrInvalid
		}
		for _, value := range services {
			service, ok := value.(map[string]any)
			if !ok {
				return plan, stack.ErrInvalid
			}
			for _, key := range []string{"build", "develop", "extends", "env_file", "configs", "secrets", "post_start", "pre_start", "pre_stop", "label_file", "credential_spec", "use_api_socket", "provider", "profiles"} {
				if _, ok := service[key]; ok {
					return plan, errors.New("this runner supports image-based stacks with encrypted interpolation; builds, includes, hooks and service file inputs need migration first")
				}
			}
			if volumes, ok := service["volumes"].([]any); ok {
				for _, v := range volumes {
					var source string
					switch mount := v.(type) {
					case string:
						source, _, _ = strings.Cut(mount, ":")
					case map[string]any:
						source, _ = mount["source"].(string)
					}
					// Parent-relative or interpolated storage paths cannot be proven equivalent in the private validation workspace.
					if strings.Contains(source, "$") || source == ".." || strings.HasPrefix(source, "../") || strings.Contains(source, "/../") {
						return plan, errors.New("bind sources must use explicit absolute paths or paths inside the approved project directory")
					}
				}
			}
		}
	}
	dir, args, e := r.workspace(ctx, s, m)
	if e != nil {
		return plan, e
	}
	defer os.RemoveAll(dir)
	if images != nil {
		override := filepath.Join(dir, ".vigara-images.json")
		if e := imageOverride(override, images); e != nil {
			return plan, e
		}
		args = append(args, "-f", override)
	}
	configArgs := append(args, "config", "--format", "json")
	if images == nil {
		// Keep the interpolated source reference before Compose pins the release.
		original, err := r.command(ctx, dir, configArgs...)
		var source struct {
			Services map[string]struct{ Image string }
		}
		if err != nil || json.Unmarshal(original, &source) != nil {
			return plan, errCompose
		}
		plan.Tracking = map[string]string{}
		for name, svc := range source.Services {
			plan.Tracking[name] = svc.Image
		}
		configArgs = append(configArgs, "--resolve-image-digests")
	}
	b, e := r.command(ctx, dir, configArgs...)
	if e != nil {
		return plan, e
	}
	var model struct {
		Networks map[string]struct {
			Name     string
			External bool
		} `json:"networks"`
		Volumes map[string]struct {
			Name     string
			External bool
		} `json:"volumes"`
		Services map[string]struct {
			Image     string                                  `json:"image"`
			Build     any                                     `json:"build"`
			Develop   any                                     `json:"develop"`
			Extends   any                                     `json:"extends"`
			EnvFile   any                                     `json:"env_file"`
			PostStart any                                     `json:"post_start"`
			PreStop   any                                     `json:"pre_stop"`
			PreStart  any                                     `json:"pre_start"`
			Volumes   []struct{ Type, Source, Target string } `json:"volumes"`
			Configs   any                                     `json:"configs"`
			Secrets   any                                     `json:"secrets"`
		} `json:"services"`
	}
	if json.Unmarshal(b, &model) != nil || len(model.Services) == 0 {
		return plan, errCompose
	}
	plan.Images = make(map[string]string, len(model.Services))
	for _, network := range model.Networks {
		if network.External {
			if _, e := r.command(ctx, dir, "network", "inspect", "--format", "{{.Name}}", network.Name); e != nil {
				return plan, errExternalNetwork
			}
		}
	}
	for _, volume := range model.Volumes {
		if volume.External {
			if _, e := r.command(ctx, dir, "volume", "inspect", "--format", "{{.Name}}", volume.Name); e != nil {
				return plan, errExternalVolume
			}
		}
	}
	for name, svc := range model.Services {
		if !immutableImage.MatchString(svc.Image) || svc.Build != nil || svc.Develop != nil || svc.Extends != nil || svc.EnvFile != nil || svc.Configs != nil || svc.Secrets != nil || svc.PostStart != nil || svc.PreStop != nil || svc.PreStart != nil {
			return plan, errUnsupportedStack
		}
		for _, v := range svc.Volumes {
			if v.Type == "bind" {
				source := v.Source
				if rel, e := filepath.Rel(dir, source); e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
					source = filepath.Join(t.Directory, rel)
				}
				if !filepath.IsAbs(source) {
					return plan, errTarget
				}
				if _, e := os.Stat(source); e != nil {
					return plan, errBindMount
				}
			}
		}
		plan.Services = append(plan.Services, name)
		plan.Images[name] = svc.Image
	}
	if !validImages(plan.Images) || (images != nil && !stack.Equal(images, plan.Images)) {
		return plan, errCompose
	}
	sort.Strings(plan.Services)
	plan.Token, e = releaseToken(t, s, plan.Images)
	return plan, e
}
func (r *Runner) Plan(ctx context.Context, s stack.Snapshot) (Plan, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, m, e := r.target(s)
	if e != nil {
		return Plan{}, e
	}
	return r.model(ctx, s, t, m)
}

func (r *Runner) Deploy(ctx context.Context, q Request) (stack.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := q.Job.Snapshot
	t, m, e := r.target(s)
	if e != nil {
		return stack.Result{}, e
	}
	if !stack.CommitPattern.MatchString(q.Job.ID) || !stack.CommitPattern.MatchString(s.Commit) || q.Token == "" || !validImages(q.Images) {
		return stack.Result{}, stack.ErrInvalid
	}
	journal := filepath.Join(t.Directory, ".vigara-jobs")
	if info, e := os.Lstat(journal); e == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return stack.Result{}, errTarget
	}
	if os.MkdirAll(journal, 0700) != nil {
		return stack.Result{}, errTarget
	}
	record := filepath.Join(journal, q.Job.ID+".json")
	if b, e := os.ReadFile(record); e == nil {
		var result stack.Result
		if json.Unmarshal(b, &result) == nil && result.JobID == q.Job.ID && result.Commit == s.Commit && result.ConfigurationRevision == stack.ConfigurationRevision(s) && result.PlanToken == q.Token && stack.Equal(result.Images, q.Images) {
			if result.Status == "running" {
				result.Status = "unknown"
				result.Stage = "reconcile"
				result.Message = "No final result recorded. Inspect the device before another deployment."
			}
			return result, nil
		}
		return stack.Result{}, errJournal
	} else if !os.IsNotExist(e) {
		return stack.Result{}, errJournal
	}
	lock := filepath.Join(journal, "active")
	if os.Mkdir(lock, 0700) != nil {
		return stack.Result{}, errors.New("another deployment is active or an interrupted deployment needs device review")
	}
	defer os.Remove(lock)
	// Another process may have finished this job between our first read and
	// acquiring the directory lock. Never replace its final/uncertain record.
	if _, e := os.Lstat(record); !os.IsNotExist(e) {
		return stack.Result{}, errJournal
	}
	result := stack.Result{Status: "failed", Stage: "validate", JobID: q.Job.ID, Commit: s.Commit, ConfigurationRevision: stack.ConfigurationRevision(s), PlanToken: q.Token, Images: q.Images, Readiness: "not-checked"}
	persist := func() bool {
		b, _ := json.Marshal(result)
		f, e := os.CreateTemp(journal, ".vigara-result-")
		if e != nil {
			return false
		}
		tmp := f.Name()
		defer os.Remove(tmp)
		_, e = f.Write(b)
		syncErr := f.Sync()
		closeErr := f.Close()
		if e != nil || syncErr != nil || closeErr != nil || os.Rename(tmp, record) != nil {
			return false
		}
		// A durable rename is essential: an acknowledged release must survive a restart
		// without allowing an uncertain apply to execute again.
		d, e := os.Open(journal)
		if e != nil {
			return false
		}
		e = d.Sync()
		closeErr = d.Close()
		return e == nil && closeErr == nil
	}
	// Record execution before any mutation. A repeated request never silently re-runs an uncertain deployment.
	result.Status = "running"
	result.Message = "Deployment started; inspect device state if no final result follows."
	if !persist() {
		return stack.Result{}, errJournal
	}
	fail := func(stage, message string) (stack.Result, error) {
		result.Status = "failed"
		result.Stage = stage
		result.Message = message
		if !persist() {
			return stack.Result{}, errJournal
		}
		return result, nil
	}
	plan, e := r.modelWithImages(ctx, s, t, m, q.Images)
	if e != nil {
		return fail("validate", e.Error())
	}
	if plan.Token != q.Token {
		return fail("validate", "Device files or resolved release inputs changed after the deployment preview. Review a new preview.")
	}
	result.Stage = "write"
	result.Message = "Saving the selected revision on this device."
	if !persist() {
		return stack.Result{}, errJournal
	}
	dir, args, e := r.workspace(ctx, s, m)
	if e != nil {
		return fail("validate", e.Error())
	}
	defer os.RemoveAll(dir)
	// Apply files only after validation, preserving the existing project directory and data paths.
	// No files or volumes are deleted, and no compose down operation is issued.
	for p, v := range s.Files {
		if safeFile(t.Directory, p) != nil {
			return fail("write", "Target path changed.")
		}
		full := filepath.Join(t.Directory, filepath.FromSlash(p))
		if os.MkdirAll(filepath.Dir(full), 0700) != nil {
			return fail("write", "Could not prepare stack files.")
		}
		temp, e := os.CreateTemp(filepath.Dir(full), ".vigara-write-")
		if e != nil {
			return fail("write", "Could not save stack files.")
		}
		tmp := temp.Name()
		_ = temp.Chmod(0600)
		_, e = temp.WriteString(v)
		closeErr := temp.Close()
		if e != nil || closeErr != nil || os.Rename(tmp, full) != nil {
			os.Remove(tmp)
			return fail("write", "Could not replace a stack file.")
		}
	}
	// Use the approved original project directory, not the validation workspace, for relative storage.
	args = []string{"compose", "-p", m.ProjectName, "--project-directory", t.Directory}
	for _, p := range m.Compose.Files {
		args = append(args, "-f", filepath.Join(t.Directory, filepath.FromSlash(p)))
	}
	for i := range m.Compose.InterpolationFiles {
		args = append(args, "--env-file", filepath.Join(dir, fmt.Sprintf(".vigara-input-%d", i)))
	}
	// Compose applies exactly the registry digests reviewed in the preview, even if tags moved.
	// Keep this non-secret override beside the journal for post-deployment inspection.
	override := filepath.Join(journal, q.Job.ID+".images.json")
	if imageOverride(override, q.Images) != nil {
		return stack.Result{}, errJournal
	}
	args = append(args, "-f", override)
	if _, e = r.command(ctx, t.Directory, append(args, "config", "--quiet")...); e != nil {
		return fail("validate", "Saved files did not pass Compose validation on this device.")
	}
	if q.Job.Pull {
		result.Stage = "pull"
		result.Message = "Downloading container images."
		if !persist() {
			return stack.Result{}, errJournal
		}
		if _, e = r.command(ctx, t.Directory, append(args, "pull", "--quiet")...); e != nil {
			return fail("pull", "Image download failed. Check registry access on this device.")
		}
	}
	result.Stage = "apply"
	result.Message = "Applying saved configuration and waiting for services."
	if !persist() {
		return stack.Result{}, errJournal
	}
	if _, e = r.command(ctx, t.Directory, append(args, "up", "--detach", "--no-build", "--pull", "never", "--wait", "--wait-timeout", "120")...); e != nil {
		result.Readiness = "compose-wait-failed"
		return fail("apply", "Compose did not confirm all services running or healthy. Inspect the stack before retrying.")
	}
	result.Status = "succeeded"
	result.Stage = "complete"
	result.Readiness = "compose-wait-passed"
	result.Message = "Compose --wait completed. Readiness follows Compose conditions; services without healthchecks may only be running. Application readiness was not independently checked."
	if !persist() {
		return stack.Result{}, errJournal
	}
	return result, nil
}
