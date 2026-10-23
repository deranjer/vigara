package stacks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/audit"
	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/internal/protocol"
	"github.com/deranjer/vigara/internal/stack"
	"github.com/deranjer/vigara/internal/stackgit"
	"github.com/jackc/pgx/v5"
	"gopkg.in/yaml.v3"
)

type gitSettings struct {
	Remote  string `json:"remote"`
	Branch  string `json:"branch"`
	Version int64  `json:"version"`
}

func (a *API) settings(ctx context.Context) (gitSettings, error) {
	s := gitSettings{Branch: "main"}
	if a.Repository != nil {
		s.Remote = a.Repository.Remote
		s.Branch = a.Repository.Branch
	}
	if a.Pool == nil {
		return s, nil
	}
	e := a.Pool.QueryRow(ctx, "SELECT remote,branch,version FROM stack_git_settings WHERE singleton=true").Scan(&s.Remote, &s.Branch, &s.Version)
	if errors.Is(e, pgx.ErrNoRows) {
		return s, nil
	}
	return s, e
}
func (a *API) repository(ctx context.Context) (*stackgit.Repository, error) {
	s, e := a.settings(ctx)
	if e != nil || s.Remote == "" {
		return nil, e
	}
	dir := os.Getenv("VIGIRA_STACK_GIT_DIR")
	if a.Repository != nil {
		dir = a.Repository.Directory
	}
	if dir == "" {
		dir = "/var/lib/vigira/stacks.git"
	}
	return &stackgit.Repository{Directory: dir, Remote: s.Remote, Branch: s.Branch}, nil
}
func (a *API) requireRepository(w http.ResponseWriter, ctx context.Context) (*stackgit.Repository, bool) {
	repo, e := a.repository(ctx)
	if e != nil {
		fail(w, e)
		return nil, false
	}
	if repo == nil {
		http.Error(w, "Connect a stack repository in Git settings first.", 503)
		return nil, false
	}
	return repo, true
}
func (a *API) getSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.Authz.Authorize(w, r, auth.PermStacksDeploy); !ok {
		return
	}
	s, e := a.settings(r.Context())
	if e != nil {
		fail(w, e)
		return
	}
	respond(w, 200, map[string]any{"remote": publicRemote(s.Remote), "branch": s.Branch, "version": s.Version, "authentication": "Server credentials", "credentialHelp": "Git uses the Vigara service account’s SSH keys or Git credential helper. Passwords and tokens are never included in repository URLs or returned to the browser."})
}

var scpRemote = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[A-Za-z0-9_./~-]+$`)

func validRemote(remote, existing string) bool {
	if remote == "" || len(remote) > 2048 || strings.ContainsAny(remote, "\r\n\x00") || strings.HasPrefix(remote, "-") {
		return false
	}
	if remote == existing && filepath.IsAbs(remote) {
		return true
	}
	if scpRemote.MatchString(remote) {
		return true
	}
	u, e := url.Parse(remote)
	if e != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	if u.Scheme == "https" {
		return u.User == nil
	}
	if u.Scheme == "ssh" {
		if u.User != nil {
			_, password := u.User.Password()
			if password {
				return false
			}
		}
		return true
	}
	return false
}
func (a *API) editSettings(w http.ResponseWriter, r *http.Request, save bool) {
	u, ok := a.Authz.Authorize(w, r, auth.PermStacksDeploy)
	if !ok {
		return
	}
	var q gitSettings
	if !decode(w, r, &q) {
		return
	}
	q.Remote = strings.TrimSpace(q.Remote)
	q.Branch = strings.TrimSpace(q.Branch)
	ctx, cancel := stackgit.DefaultTimeout(r.Context())
	defer cancel()
	unlock, e := a.repoLock(ctx)
	if e != nil {
		fail(w, e)
		return
	}
	defer unlock()
	current, e := a.settings(ctx)
	if e != nil {
		fail(w, e)
		return
	}
	if !validRemote(q.Remote, current.Remote) || len(q.Branch) > 200 || strings.ContainsAny(q.Branch, "\r\n\x00") || q.Branch == "" {
		http.Error(w, "Use an SSH or HTTPS repository URL without embedded tokens, and a valid branch. The existing local test repository can also be retained.", 422)
		return
	}
	repo, e := a.repository(ctx)
	if e != nil {
		fail(w, e)
		return
	}
	dir := os.Getenv("VIGIRA_STACK_GIT_DIR")
	if repo != nil {
		dir = repo.Directory
	}
	if dir == "" {
		dir = "/var/lib/vigira/stacks.git"
	}
	hash := sha256.Sum256([]byte(q.Remote + "\x00" + q.Branch))
	candidate := &stackgit.Repository{Directory: filepath.Join(dir, "connection-tests", hex.EncodeToString(hash[:16])), Remote: q.Remote, Branch: q.Branch}
	if _, e = candidate.List(ctx); e != nil {
		http.Error(w, "Could not read this repository and branch. Check the URL, branch and server credentials.", 422)
		return
	}
	if !save {
		respond(w, 200, map[string]string{"message": "Connected. The branch is readable. Write permission is checked when saving a stack."})
		return
	}
	if q.Version != current.Version {
		http.Error(w, "Git settings changed in another browser. Close and reopen settings.", 409)
		return
	}
	if q.Remote != current.Remote || q.Branch != current.Branch {
		var blocked bool
		e = a.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM stack_drafts) OR EXISTS(SELECT 1 FROM stack_deployments WHERE status IN ('queued','running','unknown'))").Scan(&blocked)
		if e != nil {
			fail(w, e)
			return
		}
		if blocked {
			http.Error(w, "Finish or discard saved drafts and resolve active deployments before changing the repository or branch.", 409)
			return
		}
	}
	if current.Version == 0 {
		_, e = a.Pool.Exec(ctx, "INSERT INTO stack_git_settings(singleton,remote,branch) VALUES(true,$1,$2)", q.Remote, q.Branch)
	} else {
		_, e = a.Pool.Exec(ctx, "UPDATE stack_git_settings SET remote=$1,branch=$2,version=version+1,updated_at=now() WHERE singleton=true", q.Remote, q.Branch)
	}
	if e != nil {
		fail(w, e)
		return
	}
	a.record(ctx, u, "repository", "git-settings", audit.OutcomeSuccess, map[string]any{"branch": q.Branch})
	respond(w, 200, map[string]string{"message": "Git settings saved. New saves will use this repository and branch."})
}
func (a *API) lookupTarget(ctx context.Context, id string) (Binding, bool) {
	if b, ok := a.Targets[id]; ok {
		return b, true
	}
	var b Binding
	if a.Pool == nil {
		return b, false
	}
	e := a.Pool.QueryRow(ctx, "SELECT agent_id::text,project_name,directory,display_name FROM stack_bindings WHERE stack_id=$1 AND directory<>''", id).Scan(&b.AgentID, &b.ProjectName, &b.Directory, &b.DisplayName)
	return b, e == nil
}
func (a *API) bindings(ctx context.Context) (map[string]Binding, error) {
	result := map[string]Binding{}
	for id, b := range a.Targets {
		result[id] = b
	}
	rows, e := a.Pool.Query(ctx, "SELECT stack_id,agent_id::text,project_name,directory,display_name FROM stack_bindings WHERE directory<>''")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var b Binding
		if e = rows.Scan(&id, &b.AgentID, &b.ProjectName, &b.Directory, &b.DisplayName); e != nil {
			return nil, e
		}
		if _, exists := result[id]; !exists {
			result[id] = b
		}
	}
	return result, rows.Err()
}

type setupDevice struct {
	AgentID     string `json:"agentId"`
	Name        string `json:"name"`
	ManagedRoot string `json:"managedRoot"`
	Message     string `json:"message,omitempty"`
}

func (a *API) setup(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.Authz.Authorize(w, r, auth.PermStacksDeploy); !ok {
		return
	}
	rows, e := a.Pool.Query(r.Context(), "SELECT ai.id::text,d.display_name FROM agent_identities ai JOIN devices d ON d.id=ai.device_id WHERE ai.revoked_at IS NULL")
	if e != nil {
		fail(w, e)
		return
	}
	devices := []setupDevice{}
	for rows.Next() {
		var d setupDevice
		if e = rows.Scan(&d.AgentID, &d.Name); e != nil {
			rows.Close()
			fail(w, e)
			return
		}
		devices = append(devices, d)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		fail(w, e)
		return
	}
	var group sync.WaitGroup
	semaphore := make(chan struct{}, 8)
	for i := range devices {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			select {
			case semaphore <- struct{}{}:
			case <-r.Context().Done():
				return
			}
			defer func() { <-semaphore }()
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			var info protocol.ComposeTargets
			if e := a.Hub.Call(ctx, devices[i].AgentID, protocol.MethodComposeTargets, nil, &info); e != nil {
				devices[i].Message = "Connect this device with the updated agent and Docker deployment enabled."
			} else {
				devices[i].ManagedRoot = info.ManagedRoot
				if info.ManagedRoot == "" {
					devices[i].Message = "No managed stack folder is configured on this device."
				}
			}
		}(i)
	}
	group.Wait()
	sort.Slice(devices, func(i, j int) bool { return devices[i].Name < devices[j].Name })
	respond(w, 200, map[string]any{"devices": devices})
}
func (a *API) create(w http.ResponseWriter, r *http.Request) {
	u, ok := a.authorize(w, r, true)
	if !ok {
		return
	}
	var q struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		AgentID     string `json:"agentId"`
		ProjectName string `json:"projectName"`
	}
	if !decode(w, r, &q) {
		return
	}
	q.Name = strings.TrimSpace(q.Name)
	if !stack.ValidID(q.ID) || !stack.ValidProject(q.ProjectName) || q.Name == "" || len(q.Name) > 120 {
		http.Error(w, "Enter a stack name, a lowercase stack ID and a valid Compose project name.", 422)
		return
	}
	ctx, cancel := stackgit.DefaultTimeout(r.Context())
	defer cancel()
	unlock, e := a.repoLock(ctx)
	if e != nil {
		fail(w, e)
		return
	}
	defer unlock()
	repo, ok := a.requireRepository(w, ctx)
	if !ok {
		return
	}
	if _, exists := a.lookupTarget(ctx, q.ID); exists {
		http.Error(w, "This stack ID already has a target. Open the existing stack or choose another ID.", 409)
		return
	}
	if _, e = repo.Read(ctx, q.ID, ""); !errors.Is(e, stackgit.ErrNotFound) {
		if e != nil {
			fail(w, e)
		} else {
			http.Error(w, "This stack already exists in Git.", 409)
		}
		return
	}
	var enrolled bool
	e = a.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM agent_identities WHERE id::text=$1 AND revoked_at IS NULL)", q.AgentID).Scan(&enrolled)
	if e != nil {
		fail(w, e)
		return
	}
	if !enrolled {
		http.Error(w, "Choose an enrolled device.", 422)
		return
	}
	var conflict bool
	e = a.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM stack_bindings WHERE agent_id::text=$1 AND project_name=$2)", q.AgentID, q.ProjectName).Scan(&conflict)
	if e != nil {
		fail(w, e)
		return
	}
	if conflict {
		http.Error(w, "That Compose project is already assigned on this device.", 409)
		return
	}
	var target protocol.ComposeTargetInfo
	if e = a.Hub.Call(ctx, q.AgentID, protocol.MethodComposeCreate, protocol.ComposeCreateTarget{ID: q.ID, ProjectName: q.ProjectName}, &target); e != nil {
		http.Error(w, "The device could not create this stack. It must be online with the updated agent, deployment access and a writable managed folder. Choose a new project name and an unused stack ID.", 409)
		return
	}
	if target.ID != q.ID || target.ProjectName != q.ProjectName || target.Directory == "" {
		http.Error(w, "Device target confirmation did not match.", 502)
		return
	}
	manifest := stack.Manifest{APIVersion: "vigara.io/v1", Kind: "Stack", StackID: q.ID, DisplayName: q.Name, ProjectName: q.ProjectName, ConfigurationRevisionPolicy: "sha256-git-tree-and-placement-inputs-v1"}
	manifest.Compose.Files = []string{"compose.yaml"}
	manifest.Compose.ProjectDirectory = "."
	manifest.Compose.InterpolationFiles = []stack.Input{}
	raw, _ := yaml.Marshal(manifest)
	files := map[string]string{"vigara-stack.yaml": string(raw), "compose.yaml": "services:\n  web:\n    image: nginx:alpine\n"}
	encoded, _ := json.Marshal(files)
	tx, e := a.Pool.Begin(ctx)
	if e != nil {
		fail(w, e)
		return
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, "INSERT INTO stack_bindings(stack_id,agent_id,project_name,directory,display_name) VALUES($1,$2,$3,$4,$5)", q.ID, q.AgentID, q.ProjectName, target.Directory, q.Name)
	if e == nil {
		_, e = tx.Exec(ctx, "INSERT INTO stack_drafts(stack_id,user_id,files) VALUES($1,$2,$3)", q.ID, u.ID, encoded)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		fail(w, e)
		return
	}
	a.record(ctx, u, q.ID, "create", audit.OutcomeSuccess, map[string]any{"agentId": q.AgentID, "projectName": q.ProjectName})
	respond(w, 201, map[string]string{"id": q.ID, "directory": target.Directory})
}
func serviceCount(s stack.Snapshot, m stack.Manifest) int {
	names := map[string]bool{}
	for _, p := range m.Compose.Files {
		var doc struct {
			Services map[string]any `yaml:"services"`
		}
		if yaml.Unmarshal([]byte(s.Files[p]), &doc) == nil {
			for name := range doc.Services {
				names[name] = true
			}
		}
	}
	return len(names)
}

func publicRemote(remote string) string {
	if scpRemote.MatchString(remote) {
		return remote
	}
	u, e := url.Parse(remote)
	if e != nil {
		return ""
	}
	if u.User != nil {
		if u.Scheme == "ssh" {
			u.User = url.User(u.User.Username())
		} else {
			u.User = nil
		}
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
