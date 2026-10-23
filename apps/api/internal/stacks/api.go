// Package stacks provides Git-backed drafts, saves and durable Compose deployment jobs.
package stacks

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/audit"
	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/apps/api/internal/channel"
	"github.com/deranjer/vigara/internal/composesecrets"
	"github.com/deranjer/vigara/internal/protocol"
	"github.com/deranjer/vigara/internal/stack"
	"github.com/deranjer/vigara/internal/stackgit"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Binding struct {
	AgentID     string                           `json:"agentId"`
	ProjectName string                           `json:"projectName"`
	Directory   string                           `json:"directory,omitempty"`
	DisplayName string                           `json:"displayName,omitempty"`
	Recipients  []composesecrets.RecipientRecord `json:"recipients,omitempty"`
}
type Config struct {
	Repository *stackgit.Repository
	Targets    map[string]Binding
	SOPS       string
	Pool       *pgxpool.Pool
	Hub        interface {
		Call(context.Context, string, string, any, any) error
	}
	Authz channel.Authorizer
	Audit *audit.Recorder
}
type API struct{ Config }
type draft struct {
	Base    string            `json:"base"`
	Files   map[string]string `json:"files"`
	Version int64             `json:"version"`
}
type deployment struct {
	AgentID    string     `json:"-"`
	ID         string     `json:"id"`
	StackID    string     `json:"stackId"`
	Commit     string     `json:"commit"`
	Digest     string     `json:"digest"`
	Status     string     `json:"status"`
	Stage      string     `json:"stage"`
	Message    string     `json:"message"`
	CreatedAt  time.Time  `json:"createdAt"`
	FinishedAt *time.Time `json:"finishedAt"`
}

// Configuration is service-account controlled; repository credentials never cross the browser API.
func FromEnv() (*stackgit.Repository, map[string]Binding, error) {
	targets := map[string]Binding{}
	if file := os.Getenv("VIGIRA_STACK_TARGETS_FILE"); file != "" {
		b, e := os.ReadFile(file)
		if e != nil || json.Unmarshal(b, &targets) != nil {
			return nil, nil, errors.New("invalid stack target configuration")
		}
	}
	for id, t := range targets {
		if !stack.ValidID(id) || t.AgentID == "" || t.ProjectName == "" {
			return nil, nil, errors.New("invalid stack target binding")
		}
	}
	if os.Getenv("VIGIRA_STACK_GIT_URL") == "" {
		return nil, targets, nil
	}
	dir := os.Getenv("VIGIRA_STACK_GIT_DIR")
	if dir == "" {
		dir = "/var/lib/vigira/stacks.git"
	}
	branch := os.Getenv("VIGIRA_STACK_GIT_BRANCH")
	if branch == "" {
		branch = "main"
	}
	return &stackgit.Repository{Directory: dir, Remote: os.Getenv("VIGIRA_STACK_GIT_URL"), Branch: branch}, targets, nil
}
func Register(mux *http.ServeMux, c Config) *API {
	a := &API{c}
	mux.HandleFunc("GET /api/stacks", a.list)
	mux.HandleFunc("POST /api/stacks", a.create)
	mux.HandleFunc("GET /api/stacks/settings", a.getSettings)
	mux.HandleFunc("PUT /api/stacks/settings", func(w http.ResponseWriter, r *http.Request) { a.editSettings(w, r, true) })
	mux.HandleFunc("POST /api/stacks/settings/test", func(w http.ResponseWriter, r *http.Request) { a.editSettings(w, r, false) })
	mux.HandleFunc("GET /api/stacks/setup", a.setup)
	mux.HandleFunc("GET /api/stacks/{id}", a.detail)
	mux.HandleFunc("GET /api/stacks/{id}/history", a.history)
	mux.HandleFunc("PUT /api/stacks/{id}/draft", a.putDraft)
	mux.HandleFunc("DELETE /api/stacks/{id}/draft", a.deleteDraft)
	mux.HandleFunc("POST /api/stacks/{id}/save", a.save)
	mux.HandleFunc("POST /api/stacks/{id}/environment", a.environment)
	mux.HandleFunc("POST /api/stacks/{id}/plan", a.plan)
	mux.HandleFunc("POST /api/stacks/{id}/deploy", a.deploy)
	mux.HandleFunc("GET /api/stacks/{id}/deployments", a.deployments)
	mux.HandleFunc("POST /api/stacks/{id}/deployments/{job}/reconcile", a.reconcile)
	return a
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 768<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid request", 400)
		return false
	}
	return true
}
func (a *API) authorize(w http.ResponseWriter, r *http.Request, write bool) (auth.User, bool) {
	// Stack files may contain inline credentials, so editing and reading source both require deploy permission.
	p := auth.PermStacksDeploy
	u, ok := a.Authz.Authorize(w, r, p)
	if !ok {
		return u, false
	}
	if id := r.PathValue("id"); id != "" && !stack.ValidID(id) {
		http.Error(w, "stack not found", 404)
		return u, false
	}
	return u, true
}
func fail(w http.ResponseWriter, e error) {
	switch {
	case errors.Is(e, stackgit.ErrConflict):
		http.Error(w, e.Error(), 409)
	case errors.Is(e, stackgit.ErrNotFound):
		http.Error(w, e.Error(), 404)
	case errors.Is(e, stack.ErrInvalid):
		http.Error(w, e.Error(), 422)
	default:
		http.Error(w, "Could not complete the stack operation. Check Git access and target availability.", 502)
	}
}

// One database advisory lock serializes repository mutations across API replicas.
func (a *API) repoLock(ctx context.Context) (func(), error) {
	c, e := a.Pool.Acquire(ctx)
	if e != nil {
		return nil, e
	}
	if _, e = c.Exec(ctx, "SELECT pg_advisory_lock(731946281)"); e != nil {
		c.Release()
		return nil, e
	}
	return func() {
		release, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, e := c.Exec(release, "SELECT pg_advisory_unlock(731946281)"); e != nil {
			_ = c.Conn().Close(release)
		}
		c.Release()
	}, nil
}
func (a *API) record(ctx context.Context, u auth.User, id, action, outcome string, detail map[string]any) {
	if a.Audit == nil {
		return
	}
	_ = a.Audit.Record(ctx, audit.Event{ActorID: u.ID, ActorName: u.Username, Action: "stack." + action, TargetKind: "stack", TargetID: id, Outcome: outcome, Detail: detail})
}
func (a *API) list(w http.ResponseWriter, r *http.Request) {
	u, ok := a.authorize(w, r, false)
	if !ok {
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
	repo, e := a.repository(ctx)
	if e != nil {
		fail(w, e)
		return
	}
	if repo == nil {
		respond(w, 200, map[string]any{"configured": false, "stacks": []any{}, "targets": []any{}})
		return
	}
	items, e := repo.List(ctx)
	if e != nil {
		fail(w, e)
		return
	}
	bindings, e := a.bindings(ctx)
	if e != nil {
		fail(w, e)
		return
	}
	names := map[string]string{}
	rows, e := a.Pool.Query(ctx, "SELECT ai.id::text,d.display_name FROM agent_identities ai JOIN devices d ON d.id=ai.device_id")
	if e != nil {
		fail(w, e)
		return
	}
	for rows.Next() {
		var id, name string
		if e = rows.Scan(&id, &name); e != nil {
			rows.Close()
			fail(w, e)
			return
		}
		names[id] = name
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		fail(w, e)
		return
	}
	seen := map[string]bool{}
	for _, s := range items {
		seen[s.ID] = true
	}
	for id, b := range bindings {
		if !seen[id] && b.Directory != "" {
			items = append(items, stack.Snapshot{ID: id, Files: map[string]string{}})
		}
	}
	summaries := []map[string]any{}
	for _, s := range items {
		m, validation := stack.Validate(s)
		b, assigned := bindings[s.ID]
		name := m.DisplayName
		if name == "" {
			name = b.DisplayName
		}
		if name == "" {
			name = s.ID
		}
		project := m.ProjectName
		if project == "" {
			project = b.ProjectName
		}
		latest := deployment{}
		e = a.Pool.QueryRow(ctx, "SELECT commit_id,status,created_at,configuration_digest FROM stack_deployments WHERE stack_id=$1 ORDER BY created_at DESC LIMIT 1", s.ID).Scan(&latest.Commit, &latest.Status, &latest.CreatedAt, &latest.Digest)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			fail(w, e)
			return
		}
		var draftPresent bool
		e = a.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM stack_drafts WHERE stack_id=$1 AND user_id=$2)", s.ID, u.ID).Scan(&draftPresent)
		if e != nil {
			fail(w, e)
			return
		}
		summaries = append(summaries, map[string]any{"id": s.ID, "name": name, "projectName": project, "commit": s.Commit, "digest": stack.Digest(s), "valid": validation == nil, "assigned": assigned, "agentId": b.AgentID, "deviceName": names[b.AgentID], "directory": b.Directory, "serviceCount": serviceCount(s, m), "draft": draftPresent, "deploymentStatus": latest.Status, "deploymentCommit": latest.Commit, "deploymentDigest": latest.Digest, "deploymentAt": latest.CreatedAt})
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i]["name"].(string) < summaries[j]["name"].(string) })
	targets := []map[string]string{}
	for id, b := range a.Targets {
		targets = append(targets, map[string]string{"id": id, "projectName": b.ProjectName, "agentId": b.AgentID, "deviceName": names[b.AgentID]})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i]["id"] < targets[j]["id"] })
	respond(w, 200, map[string]any{"configured": true, "branch": repo.Branch, "stacks": summaries, "targets": targets})
}

func (a *API) detail(w http.ResponseWriter, r *http.Request) {
	u, ok := a.authorize(w, r, false)
	if !ok {
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
	s, e := repo.Read(ctx, r.PathValue("id"), r.URL.Query().Get("commit"))
	if errors.Is(e, stackgit.ErrNotFound) && r.URL.Query().Get("commit") == "" {
		if _, allowed := a.lookupTarget(ctx, r.PathValue("id")); allowed {
			s = stack.Snapshot{ID: r.PathValue("id"), Files: map[string]string{}}
			e = nil
		}
	}
	if e != nil {
		fail(w, e)
		return
	}
	d := draft{}
	var raw []byte
	e = a.Pool.QueryRow(ctx, "SELECT base_commit,files,version FROM stack_drafts WHERE stack_id=$1 AND user_id=$2", s.ID, u.ID).Scan(&d.Base, &raw, &d.Version)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		fail(w, e)
		return
	}
	if raw != nil {
		_ = json.Unmarshal(raw, &d.Files)
	}
	b, assigned := a.lookupTarget(ctx, s.ID)
	respond(w, 200, map[string]any{"snapshot": s, "draft": d, "assigned": assigned, "agentId": b.AgentID, "projectName": b.ProjectName, "branch": repo.Branch, "digest": stack.Digest(s)})
}
func (a *API) history(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.authorize(w, r, false); !ok {
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
	h, e := repo.History(ctx, r.PathValue("id"))
	if e != nil {
		fail(w, e)
		return
	}
	respond(w, 200, h)
}
func validDraft(id string, d draft) bool {
	if d.Base != "" && !stack.CommitPattern.MatchString(d.Base) || len(d.Files) == 0 || len(d.Files) > 64 {
		return false
	}
	n := 0
	for p, v := range d.Files {
		if !stack.ValidPath(p) || len(v) > 128<<10 {
			return false
		}
		n += len(p) + len(v)
	}
	return n <= stack.MaxBytes
}
func (a *API) putDraft(w http.ResponseWriter, r *http.Request) {
	u, ok := a.authorize(w, r, true)
	if !ok {
		return
	}
	var d draft
	if !decode(w, r, &d) {
		return
	}
	id := r.PathValue("id")
	if !validDraft(id, d) {
		http.Error(w, "invalid draft", 422)
		return
	}
	// Even incomplete drafts must not persist plaintext dotenv inputs.
	for p := range d.Files {
		if strings.EqualFold(p, ".env") || strings.HasSuffix(strings.ToLower(p), ".env") {
			http.Error(w, "Use the encrypted environment editor for dotenv files.", 422)
			return
		}
	}
	unlock, e := a.repoLock(r.Context())
	if e != nil {
		fail(w, e)
		return
	}
	defer unlock()
	b, _ := json.Marshal(d.Files)
	e = a.Pool.QueryRow(r.Context(), `INSERT INTO stack_drafts(stack_id,user_id,base_commit,files) SELECT $1,$2,$3,$4 WHERE $5::bigint=0
 ON CONFLICT(stack_id,user_id) DO UPDATE SET base_commit=$3,files=$4,version=stack_drafts.version+1,updated_at=now() WHERE stack_drafts.version=$5 RETURNING version`, id, u.ID, d.Base, b, d.Version).Scan(&d.Version)
	// Existing drafts with version > 0 use a separate conditional UPDATE (INSERT SELECT would produce no row).
	if errors.Is(e, pgx.ErrNoRows) && d.Version > 0 {
		e = a.Pool.QueryRow(r.Context(), `UPDATE stack_drafts SET base_commit=$3,files=$4,version=version+1,updated_at=now() WHERE stack_id=$1 AND user_id=$2 AND version=$5 RETURNING version`, id, u.ID, d.Base, b, d.Version).Scan(&d.Version)
	}
	if errors.Is(e, pgx.ErrNoRows) {
		http.Error(w, "Draft changed in another browser tab. Reload before editing.", 409)
		return
	}
	if e != nil {
		fail(w, e)
		return
	}
	respond(w, 200, d)
}
func (a *API) deleteDraft(w http.ResponseWriter, r *http.Request) {
	u, ok := a.authorize(w, r, true)
	if !ok {
		return
	}
	var q struct {
		Version int64 `json:"version"`
	}
	if !decode(w, r, &q) {
		return
	}
	tag, e := a.Pool.Exec(r.Context(), "DELETE FROM stack_drafts WHERE stack_id=$1 AND user_id=$2 AND version=$3", r.PathValue("id"), u.ID, q.Version)
	if e != nil {
		fail(w, e)
		return
	}
	if tag.RowsAffected() != 1 {
		http.Error(w, "Draft changed. Reload before discarding.", 409)
		return
	}
	respond(w, 200, map[string]bool{"discarded": true})
}
func (a *API) readDraft(ctx context.Context, id, user string, version int64) (stack.Snapshot, error) {
	s := stack.Snapshot{ID: id}
	var b []byte
	var v int64
	e := a.Pool.QueryRow(ctx, "SELECT base_commit,files,version FROM stack_drafts WHERE stack_id=$1 AND user_id=$2", id, user).Scan(&s.Commit, &b, &v)
	if e != nil {
		return s, e
	}
	if version != v {
		return s, stackgit.ErrConflict
	}
	e = json.Unmarshal(b, &s.Files)
	return s, e
}
func (a *API) binding(ctx context.Context, id, project string) (Binding, error) {
	b, ok := a.lookupTarget(ctx, id)
	if !ok || b.ProjectName != project {
		return b, errors.New("stack target not configured")
	}
	_, e := a.Pool.Exec(ctx, "INSERT INTO stack_bindings(stack_id,agent_id,project_name) VALUES($1,$2,$3) ON CONFLICT(stack_id) DO NOTHING", id, b.AgentID, project)
	if e != nil {
		return b, e
	}
	var agent, name string
	e = a.Pool.QueryRow(ctx, "SELECT agent_id::text,project_name FROM stack_bindings WHERE stack_id=$1", id).Scan(&agent, &name)
	if e != nil || agent != b.AgentID || name != project {
		return b, errors.New("stack ownership changed; an explicit fenced transfer is required")
	}
	return b, nil
}
func (a *API) validate(ctx context.Context, s stack.Snapshot) (stack.Plan, error) {
	var plan stack.Plan
	m, e := stack.Validate(s)
	if e != nil {
		return plan, e
	}
	b, e := a.binding(ctx, s.ID, m.ProjectName)
	if e != nil {
		return plan, e
	}
	e = a.Hub.Call(ctx, b.AgentID, protocol.MethodComposeValidate, s, &plan)
	return plan, e
}
func (a *API) save(w http.ResponseWriter, r *http.Request) {
	u, ok := a.authorize(w, r, true)
	if !ok {
		return
	}
	var q struct {
		Version int64 `json:"version"`
	}
	if !decode(w, r, &q) {
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
	s, e := a.readDraft(ctx, r.PathValue("id"), u.ID, q.Version)
	if e != nil {
		fail(w, e)
		return
	}
	if _, e = a.validate(ctx, s); e != nil {
		a.record(ctx, u, s.ID, "save", audit.OutcomeFailure, nil)
		fail(w, e)
		return
	}
	saved, e := repo.Save(ctx, s, u.Username)
	if e != nil {
		a.record(ctx, u, s.ID, "save", audit.OutcomeFailure, nil)
		fail(w, e)
		return
	}
	if saved.State == "saved" {
		_, e = a.Pool.Exec(ctx, "DELETE FROM stack_drafts WHERE stack_id=$1 AND user_id=$2 AND version=$3", s.ID, u.ID, q.Version)
		if e != nil {
			fail(w, e)
			return
		}
	}
	outcome := audit.OutcomeSuccess
	if saved.State != "saved" {
		outcome = audit.OutcomeFailure
	}
	a.record(ctx, u, s.ID, "save", outcome, map[string]any{"commit": saved.Commit, "state": saved.State})
	respond(w, 200, saved)
}
func (a *API) environment(w http.ResponseWriter, r *http.Request) {
	_, ok := a.authorize(w, r, true)
	if !ok {
		return
	}
	var q struct {
		Path     string `json:"path"`
		Value    string `json:"value"`
		Manifest string `json:"manifest"`
	}
	if !decode(w, r, &q) {
		return
	}
	if !stack.ValidPath(q.Path) || len(q.Path) < 5 || q.Path[len(q.Path)-5:] != ".sops" {
		http.Error(w, "Select an encrypted environment file ending in .sops.", 422)
		return
	}
	b, ok := a.lookupTarget(r.Context(), r.PathValue("id"))
	if !ok {
		http.Error(w, "stack target not configured", 422)
		return
	}
	cipher, e := composesecrets.EncryptForPolicy(r.Context(), a.SOPS, []byte(q.Value), b.Recipients)
	if e != nil {
		http.Error(w, "Environment encryption failed. Configure editor, target and recovery recipients.", 422)
		return
	}
	refs := []stack.RecipientRef{}
	for _, recipient := range b.Recipients {
		refs = append(refs, stack.RecipientRef{Class: string(recipient.Class), KeyRef: recipient.KeyRef})
	}
	manifest, e := stack.WithEncryptedInput(q.Manifest, q.Path, refs)
	if e != nil {
		http.Error(w, "The stack manifest must contain a valid compose section before adding environment inputs.", 422)
		return
	}
	respond(w, 200, map[string]string{"path": q.Path, "ciphertext": string(cipher), "manifest": manifest})
}
func (a *API) plan(w http.ResponseWriter, r *http.Request) {
	_, ok := a.authorize(w, r, true)
	if !ok {
		return
	}
	var q struct {
		Commit string `json:"commit"`
	}
	if !decode(w, r, &q) {
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
	if !stack.CommitPattern.MatchString(q.Commit) {
		http.Error(w, "Select a saved revision.", 422)
		return
	}
	s, e := repo.Read(ctx, r.PathValue("id"), q.Commit)
	if e != nil {
		fail(w, e)
		return
	}
	plan, e := a.validate(ctx, s)
	if e != nil {
		fail(w, e)
		return
	}
	respond(w, 200, plan)
}
func (a *API) deploy(w http.ResponseWriter, r *http.Request) {
	u, ok := a.authorize(w, r, true)
	if !ok {
		return
	}
	var q struct {
		Commit string `json:"commit"`
		Token  string `json:"token"`
		Pull   bool   `json:"pull"`
	}
	if !decode(w, r, &q) {
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
	if !stack.CommitPattern.MatchString(q.Commit) || len(q.Token) != 64 {
		http.Error(w, "Review a deployment preview first.", 422)
		return
	}
	s, e := repo.Read(ctx, r.PathValue("id"), q.Commit)
	if e != nil {
		fail(w, e)
		return
	}
	m, e := stack.Validate(s)
	if e != nil {
		fail(w, e)
		return
	}
	b, e := a.binding(ctx, s.ID, m.ProjectName)
	if e != nil {
		fail(w, e)
		return
	}
	// Refuse plans that became stale while the user was reviewing them.
	plan, e := a.validate(ctx, s)
	if e != nil {
		fail(w, e)
		return
	}
	if plan.Token != q.Token {
		http.Error(w, "Device files changed. Review a fresh deployment preview.", 409)
		return
	}
	random := make([]byte, 32)
	if _, e = rand.Read(random); e != nil {
		fail(w, e)
		return
	}
	id := hex.EncodeToString(random)
	raw, _ := json.Marshal(s)
	_, e = a.Pool.Exec(ctx, `INSERT INTO stack_deployments(id,stack_id,agent_id,project_name,actor_id,actor_name,commit_id,configuration_digest,snapshot,plan_token,pull,status) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'queued')`, id, s.ID, b.AgentID, m.ProjectName, u.ID, u.Username, s.Commit, stack.Digest(s), raw, q.Token, q.Pull)
	if e != nil {
		http.Error(w, "An active or uncertain deployment already exists, or the deployment could not be recorded. Refresh deployment history.", 409)
		return
	}
	a.record(ctx, u, s.ID, "deploy", audit.OutcomeSuccess, map[string]any{"job": id, "commit": s.Commit})
	respond(w, 202, map[string]string{"id": id, "status": "queued"})
}
func (a *API) deployments(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.authorize(w, r, false); !ok {
		return
	}
	items := []deployment{}
	rows, e := a.Pool.Query(r.Context(), `SELECT id,stack_id,commit_id,configuration_digest,status,stage,message,created_at,finished_at,agent_id::text FROM stack_deployments WHERE stack_id=$1 ORDER BY created_at DESC LIMIT 30`, r.PathValue("id"))
	if e != nil {
		fail(w, e)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var d deployment
		if e = rows.Scan(&d.ID, &d.StackID, &d.Commit, &d.Digest, &d.Status, &d.Stage, &d.Message, &d.CreatedAt, &d.FinishedAt, &d.AgentID); e != nil {
			fail(w, e)
			return
		}
		items = append(items, d)
	}
	if rows.Err() != nil {
		fail(w, rows.Err())
		return
	}
	for i := range items {
		if items[i].Status != "running" {
			continue
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		var result stack.Result
		err := a.Hub.Call(ctx, items[i].AgentID, protocol.MethodComposeStatus, map[string]string{"stackId": items[i].StackID, "jobId": items[i].ID}, &result)
		cancel()
		if err == nil && result.Status == "running" {
			items[i].Stage = result.Stage
			items[i].Message = result.Message
		}
	}
	respond(w, 200, items)
}
func (a *API) reconcile(w http.ResponseWriter, r *http.Request) {
	u, ok := a.authorize(w, r, true)
	if !ok {
		return
	}
	var agent string
	e := a.Pool.QueryRow(r.Context(), "SELECT agent_id::text FROM stack_deployments WHERE id=$1 AND stack_id=$2 AND status='unknown'", r.PathValue("job"), r.PathValue("id")).Scan(&agent)
	if e != nil {
		http.Error(w, "No uncertain deployment found.", 404)
		return
	}
	var result stack.Result
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	e = a.Hub.Call(ctx, agent, protocol.MethodComposeStatus, map[string]string{"stackId": r.PathValue("id"), "jobId": r.PathValue("job")}, &result)
	if e != nil {
		fail(w, e)
		return
	}
	if result.Status == "succeeded" || result.Status == "failed" {
		_, e = a.Pool.Exec(ctx, "UPDATE stack_deployments SET status=$2,stage=$3,message=$4,finished_at=now() WHERE id=$1 AND status='unknown'", r.PathValue("job"), result.Status, result.Stage, result.Message)
		if e != nil {
			fail(w, e)
			return
		}
	}
	a.record(ctx, u, r.PathValue("id"), "reconcile", audit.OutcomeSuccess, map[string]any{"job": r.PathValue("job"), "status": result.Status})
	respond(w, 200, result)
}

// Run claims durable jobs using SKIP LOCKED. A session advisory lock prevents another
// worker from classifying an executing job as interrupted. Jobs are never auto-replayed.
func (a *API) Run(ctx context.Context) {
	timer := time.NewTicker(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			a.tick(ctx)
		}
	}
}
func (a *API) tick(ctx context.Context) {
	c, e := a.Pool.Acquire(ctx)
	if e != nil {
		return
	}
	defer c.Release()
	var locked bool
	if c.QueryRow(ctx, "SELECT pg_try_advisory_lock(731946282)").Scan(&locked) != nil || !locked {
		return
	}
	defer func() {
		release, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, e := c.Exec(release, "SELECT pg_advisory_unlock(731946282)"); e != nil {
			_ = c.Conn().Close(release)
		}
	}()
	_, e = c.Exec(ctx, "UPDATE stack_deployments SET status='unknown',stage='reconcile',message='Server restarted or lost the deployment result. Check device status before another deployment.' WHERE status='running'")
	if e != nil {
		return
	}
	var id, agent, token, stackID string
	var raw []byte
	var pull bool
	e = c.QueryRow(ctx, `UPDATE stack_deployments SET status='running',stage='validate' WHERE id=(SELECT id FROM stack_deployments WHERE status='queued' ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING id,agent_id::text,snapshot,plan_token,pull,stack_id`).Scan(&id, &agent, &raw, &token, &pull, &stackID)
	if e != nil {
		return
	}
	var s stack.Snapshot
	if json.Unmarshal(raw, &s) != nil {
		return
	}
	var result stack.Result
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	e = a.Hub.Call(callCtx, agent, protocol.MethodComposeDeploy, stack.DeployRequest{Job: stack.Job{ID: id, Snapshot: s, Pull: pull}, Token: token}, &result)
	cancel()
	if e != nil {
		result = stack.Result{Status: "unknown", Stage: "reconcile", Message: "Device disconnected or did not confirm the outcome. Check device status before another deployment."}
		var channelError *protocol.ChannelError
		if errors.Is(e, channel.ErrAgentOffline) {
			result = stack.Result{Status: "failed", Stage: "connect", Message: "The device is offline. No deployment was sent."}
		}
		if errors.As(e, &channelError) && (channelError.Code == protocol.ErrCodeUnknownMethod || channelError.Code == protocol.ErrCodeTierInsufficient || channelError.Code == protocol.ErrCodeBadRequest) {
			result = stack.Result{Status: "failed", Stage: "validate", Message: "The device refused the deployment. Check its agent version, approved target and deploy permission."}
		}
	}
	if result.Status != "succeeded" && result.Status != "failed" {
		result.Status = "unknown"
	}
	finish, finishCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer finishCancel()
	_, _ = c.Exec(finish, "UPDATE stack_deployments SET status=$2,stage=$3,message=$4,finished_at=CASE WHEN $2='unknown' THEN NULL ELSE now() END WHERE id=$1", id, result.Status, result.Stage, result.Message)
}
