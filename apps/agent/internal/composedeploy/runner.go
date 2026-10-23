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
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/deranjer/vigara/apps/agent/internal/channel"
	"github.com/deranjer/vigara/internal/composesecrets"
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

func Register(c *channel.Client, r *Runner) {
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
		return r.Plan(ctx, s)
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
			return stack.Result{Status: status, Stage: "validate", Message: err.Error()}, nil
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
	m, e := stack.Validate(s)
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
	plan := Plan{ProjectName: m.ProjectName, Services: []string{}}
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
			for _, key := range []string{"build", "develop", "extends", "env_file", "configs", "secrets", "post_start", "pre_start", "pre_stop", "label_file", "credential_spec", "use_api_socket", "provider"} {
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
	b, e := r.command(ctx, dir, append(args, "config", "--format", "json")...)
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
	for _, network := range model.Networks {
		if network.External {
			if _, e := r.command(ctx, dir, "network", "inspect", "--format", "{{.Name}}", network.Name); e != nil {
				return plan, errors.New("a required external network is unavailable")
			}
		}
	}
	for _, volume := range model.Volumes {
		if volume.External {
			if _, e := r.command(ctx, dir, "volume", "inspect", "--format", "{{.Name}}", volume.Name); e != nil {
				return plan, errors.New("a required external volume is unavailable")
			}
		}
	}
	for name, svc := range model.Services {
		if svc.Image == "" || svc.Build != nil || svc.Develop != nil || svc.Extends != nil || svc.EnvFile != nil || svc.Configs != nil || svc.Secrets != nil || svc.PostStart != nil || svc.PreStop != nil || svc.PreStart != nil {
			return plan, errors.New("this runner supports image-based stacks with interpolation inputs; builds, hooks and service file inputs need migration first")
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
					return plan, errors.New("a required bind mount is missing on this device")
				}
			}
		}
		plan.Services = append(plan.Services, name)
	}
	sort.Strings(plan.Services)
	plan.Token, e = fingerprint(t, s)
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
	if !stack.CommitPattern.MatchString(q.Job.ID) || !stack.CommitPattern.MatchString(s.Commit) || q.Token == "" {
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
		if json.Unmarshal(b, &result) == nil {
			return result, nil
		}
	}
	lock := filepath.Join(journal, "active")
	if os.Mkdir(lock, 0700) != nil {
		return stack.Result{}, errors.New("another deployment is active or an interrupted deployment needs device review")
	}
	defer os.Remove(lock)
	result := stack.Result{Status: "failed", Stage: "validate"}
	persist := func() bool {
		b, _ := json.Marshal(result)
		tmp := record + ".tmp"
		f, e := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if e != nil {
			return false
		}
		_, e = f.Write(b)
		syncErr := f.Sync()
		closeErr := f.Close()
		if e != nil || syncErr != nil || closeErr != nil || os.Rename(tmp, record) != nil {
			return false
		}
		return true
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
	plan, e := r.model(ctx, s, t, m)
	if e != nil {
		return fail("validate", e.Error())
	}
	if plan.Token != q.Token {
		return fail("validate", "Device files changed after the deployment preview. Review a new preview.")
	}
	result.Stage = "write"
	result.Message = "Saving the selected revision on this device."
	persist()
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
	if _, e = r.command(ctx, t.Directory, append(args, "config", "--quiet")...); e != nil {
		return fail("validate", "Saved files did not pass Compose validation on this device.")
	}
	if q.Job.Pull {
		result.Stage = "pull"
		result.Message = "Downloading container images."
		persist()
		if _, e = r.command(ctx, t.Directory, append(args, "pull", "--quiet")...); e != nil {
			return fail("pull", "Image download failed. Check registry access on this device.")
		}
	}
	result.Stage = "apply"
	result.Message = "Applying saved configuration and waiting for services."
	persist()
	if _, e = r.command(ctx, t.Directory, append(args, "up", "--detach", "--no-build", "--wait", "--wait-timeout", "120")...); e != nil {
		return fail("apply", "Compose did not confirm all services running or healthy. Inspect the stack before retrying.")
	}
	result.Status = "succeeded"
	result.Stage = "complete"
	result.Message = "Compose confirmed services running or healthy."
	if !persist() {
		return stack.Result{}, errJournal
	}
	return result, nil
}
