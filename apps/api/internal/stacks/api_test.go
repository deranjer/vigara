package stacks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/apps/api/migrations"
	"github.com/deranjer/vigara/internal/protocol"
	"github.com/deranjer/vigara/internal/stack"
	"github.com/deranjer/vigara/internal/stackgit"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type authorizer struct{ user auth.User }

func TestRepositoryLockWaitLeavesPoolCapacity(t *testing.T) {
	config := testPool(t).Config()
	config.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	a := &API{Config: Config{Pool: pool}}
	unlock, err := a.repoLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if unlock != nil {
			unlock()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	done := make(chan error, 1)
	before := pool.Stat().AcquireCount()
	go func() {
		release, err := a.repoLock(ctx)
		if err == nil {
			release()
		}
		done <- err
	}()
	defer func() { cancel(); <-done }()
	for pool.Stat().AcquireCount() == before {
		select {
		case <-ctx.Done():
			t.Fatal("lock waiter never acquired a connection")
		case <-time.After(10 * time.Millisecond):
		}
	}
	queryCtx, queryCancel := context.WithTimeout(context.Background(), time.Second)
	defer queryCancel()
	if err := pool.Ping(queryCtx); err != nil {
		t.Fatalf("a waiting repository request exhausted the pool and blocked the lock holder's queries: %v", err)
	}
	select {
	case <-done:
		// Restore the signal so cleanup does not block.
		done <- nil
		t.Fatal("repository lock did not exclude a second holder")
	default:
	}
	unlock()
	unlock = nil
	select {
	case err := <-done:
		done <- err
		if err != nil {
			t.Fatalf("waiting request did not acquire the released lock: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("waiting request did not acquire the released lock")
	}
}

func (a authorizer) Authorize(w http.ResponseWriter, r *http.Request, p auth.Permission) (auth.User, bool) {
	if !a.user.Can(p) {
		http.Error(w, "forbidden", 403)
		return a.user, false
	}
	return a.user, true
}

type fakeAgent struct{ deployed []stack.DeployRequest }

func (f *fakeAgent) Call(ctx context.Context, agent, method string, params, out any) error {
	switch method {
	case protocol.MethodComposeTargets:
		*out.(*protocol.ComposeTargets) = protocol.ComposeTargets{ManagedRoot: "/srv/managed", Targets: []protocol.ComposeTargetInfo{}}
	case protocol.MethodComposeCreate:
		q := params.(protocol.ComposeCreateTarget)
		*out.(*protocol.ComposeTargetInfo) = protocol.ComposeTargetInfo{ID: q.ID, ProjectName: q.ProjectName, Directory: "/srv/managed/" + q.ID}
	case protocol.MethodComposeValidate:
		*out.(*stack.Plan) = stack.Plan{Token: strings.Repeat("d", 64), ProjectName: "demo", Services: []string{"web"}, Images: map[string]string{"web": "nginx@sha256:" + strings.Repeat("a", 64)}}
	case protocol.MethodComposeDeploy:
		f.deployed = append(f.deployed, params.(stack.DeployRequest))
		q := params.(stack.DeployRequest)
		*out.(*stack.Result) = stack.Result{Status: "succeeded", Stage: "complete", Message: "Running", JobID: q.Job.ID, Commit: q.Job.Snapshot.Commit, ConfigurationRevision: stack.ConfigurationRevision(q.Job.Snapshot), PlanToken: q.Token, Images: q.Images, Readiness: "compose-wait-passed"}
	case protocol.MethodComposeStatus:
		if len(f.deployed) > 0 {
			q := f.deployed[len(f.deployed)-1]
			*out.(*stack.Result) = stack.Result{Status: "succeeded", Stage: "complete", Message: "Recovered result", JobID: q.Job.ID, Commit: q.Job.Snapshot.Commit, ConfigurationRevision: stack.ConfigurationRevision(q.Job.Snapshot), PlanToken: q.Token, Images: q.Images, Readiness: "compose-wait-passed"}
		}
	default:
		return fmt.Errorf("unexpected method %s", method)
	}
	return nil
}
func TestSaveDeployPinnedRevisionAndRecoverUnknown(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	root := t.TempDir()
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@localhost", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@localhost")
		b, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("git: %v %s", e, b)
		}
		return strings.TrimSpace(string(b))
	}
	remote := filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	git(root, "init", "--bare", remote)
	git(root, "init", "-b", "main", seed)
	if e := os.WriteFile(filepath.Join(seed, "README.md"), []byte("Fixture"), 0600); e != nil {
		t.Fatal(e)
	}
	git(seed, "add", "README.md")
	git(seed, "commit", "-m", "Initialize")
	git(seed, "push", remote, "main")
	repo := &stackgit.Repository{Directory: filepath.Join(root, "cache.git"), Remote: remote, Branch: "main"}
	s := stack.Snapshot{ID: "demo", Files: map[string]string{"vigara-stack.yaml": "apiVersion: vigara.io/v1\nkind: Stack\nstackId: demo\ndisplayName: Demo\nprojectName: demo\ncompose:\n  files: [compose.yaml]\n  projectDirectory: .\n  interpolationFiles: []\nconfigurationRevisionPolicy: sha256-git-tree-and-placement-inputs-v1\n", "compose.yaml": "services:\n  web:\n    image: nginx\n"}}
	initial, e := repo.Save(ctx, s, "fixture")
	if e != nil {
		t.Fatal(e)
	}
	var user, device, agent string
	if e = pool.QueryRow(ctx, "INSERT INTO users(username,display_name,password_hash) VALUES('owner','Owner','test-only') RETURNING id::text").Scan(&user); e != nil {
		t.Fatal(e)
	}
	if e = pool.QueryRow(ctx, "INSERT INTO devices(display_name) VALUES('Test target') RETURNING id::text").Scan(&device); e != nil {
		t.Fatal(e)
	}
	if e = pool.QueryRow(ctx, "INSERT INTO agent_identities(device_id,credential_hash,protocol_version) VALUES($1,$2,'v1') RETURNING id::text", device, []byte("test-credential")).Scan(&agent); e != nil {
		t.Fatal(e)
	}
	hub := &fakeAgent{}
	mux := http.NewServeMux()
	api := Register(mux, Config{Pool: pool, Repository: repo, Hub: hub, Targets: map[string]Binding{"demo": {AgentID: agent, ProjectName: "demo"}}, Authz: authorizer{auth.User{ID: user, Username: "owner", Role: auth.RoleAdmin}}})
	call := func(method, path string, body any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(string(b))))
		return w
	}
	s.Commit = initial.Commit
	s.Files["compose.yaml"] = "services:\n  web:\n    image: nginx:stable\n"
	w := call("PUT", "/api/stacks/demo/draft", draft{Base: s.Commit, Files: s.Files})
	if w.Code != 200 {
		t.Fatalf("draft %d %s", w.Code, w.Body)
	}
	w = call("POST", "/api/stacks/demo/save", map[string]int{"version": 1})
	if w.Code != 200 {
		t.Fatalf("save %d %s", w.Code, w.Body)
	}
	var saved stackgit.Saved
	_ = json.Unmarshal(w.Body.Bytes(), &saved)
	if len(hub.deployed) != 0 {
		t.Fatal("Save deployed a stack")
	}
	if w = call("POST", "/api/stacks/demo/plan", map[string]any{"commit": saved.Commit}); w.Code != 200 {
		t.Fatalf("preview %d %s", w.Code, w.Body)
	}
	w = call("POST", "/api/stacks/demo/deploy", map[string]any{"commit": saved.Commit, "token": strings.Repeat("d", 64), "pull": false})
	if w.Code != 202 {
		t.Fatalf("deploy %d %s", w.Code, w.Body)
	}
	if w = call("POST", "/api/stacks/demo/deploy", map[string]any{"commit": saved.Commit, "token": strings.Repeat("d", 64)}); w.Code != 409 {
		t.Fatalf("concurrent deployment accepted: %d", w.Code)
	}
	// A newer save must never change the queued job's captured source.
	s.Commit = saved.Commit
	s.Files["compose.yaml"] = "services:\n  web:\n    image: nginx:1.27\n"
	if _, e = repo.Save(ctx, s, "next save"); e != nil {
		t.Fatal(e)
	}
	api.tick(ctx)
	if len(hub.deployed) != 1 || hub.deployed[0].Job.Snapshot.Commit != saved.Commit || hub.deployed[0].Job.Snapshot.Files["compose.yaml"] == s.Files["compose.yaml"] {
		t.Fatal("queued deployment was not pinned")
	}
	var status string
	if e = pool.QueryRow(ctx, "SELECT status FROM stack_deployments").Scan(&status); e != nil || status != "succeeded" {
		t.Fatalf("job %s %v", status, e)
	}
	job := hub.deployed[0].Job.ID
	if _, e = pool.Exec(ctx, "UPDATE stack_deployments SET status='running',finished_at=NULL WHERE id=$1", job); e != nil {
		t.Fatal(e)
	}
	api.tick(ctx)
	if len(hub.deployed) != 1 {
		t.Fatal("interrupted deployment was replayed")
	}
	if e = pool.QueryRow(ctx, "SELECT status FROM stack_deployments WHERE id=$1", job).Scan(&status); e != nil || status != "unknown" {
		t.Fatalf("restart %s %v", status, e)
	}
	w = call("POST", "/api/stacks/demo/deployments/"+job+"/reconcile", map[string]any{})
	if w.Code != 200 {
		t.Fatalf("reconcile %d %s", w.Code, w.Body)
	}
	if e = pool.QueryRow(ctx, "SELECT status FROM stack_deployments WHERE id=$1", job).Scan(&status); e != nil || status != "succeeded" {
		t.Fatalf("reconcile %s %v", status, e)
	}
}
func TestStacksRequireDeployPermission(t *testing.T) {
	for _, role := range []string{auth.RoleViewer, auth.RoleOperator} {
		mux := http.NewServeMux()
		Register(mux, Config{Authz: authorizer{auth.User{Role: role}}})
		for _, endpoint := range []string{"/api/stacks", "/api/stacks/demo", "/api/stacks/demo/history", "/api/stacks/demo/deployments"} {
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest("GET", endpoint, nil))
			if w.Code != 403 {
				t.Fatalf("%s %s: %d", role, endpoint, w.Code)
			}
		}
	}
}
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	raw := os.Getenv("VIGIRA_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set VIGIRA_TEST_DATABASE_URL for PostgreSQL integration tests")
	}
	ctx := context.Background()
	admin, e := pgx.Connect(ctx, raw)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = admin.Exec(ctx, `BEGIN; SELECT pg_advisory_xact_lock(138139136); CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public; COMMIT;`); e != nil {
		t.Fatal(e)
	}
	schema := fmt.Sprintf("stack_test_%d", time.Now().UnixNano())
	if _, e = admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		_ = admin.Close(context.Background())
	})
	u, e := url.Parse(raw)
	if e != nil {
		t.Fatal(e)
	}
	q := u.Query()
	q.Set("search_path", schema+",public")
	u.RawQuery = q.Encode()
	if e = migrations.Apply(u.String()); e != nil {
		t.Fatal(e)
	}
	pool, e := pgxpool.New(ctx, u.String())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(pool.Close)
	return pool
}
func TestDraftPersistenceAndOptimisticConcurrency(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	var id string
	if e := pool.QueryRow(ctx, "INSERT INTO users(username,display_name,password_hash) VALUES('stack-test','Stack test','test-only') RETURNING id::text").Scan(&id); e != nil {
		t.Fatal(e)
	}
	mux := http.NewServeMux()
	Register(mux, Config{Pool: pool, Repository: &stackgit.Repository{}, Authz: authorizer{auth.User{ID: id, Username: "stack-test", Role: auth.RoleAdmin}}})
	call := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}
	body := `{"base":"","files":{"compose.yaml":"services: {}"},"version":0}`
	first := call("PUT", "/api/stacks/demo/draft", body)
	if first.Code != 200 {
		t.Fatalf("create: %d %s", first.Code, first.Body)
	}
	var d draft
	if json.Unmarshal(first.Body.Bytes(), &d) != nil || d.Version != 1 {
		t.Fatal("invalid first draft")
	}
	if w := call("PUT", "/api/stacks/demo/draft", body); w.Code != 409 {
		t.Fatalf("stale create: %d", w.Code)
	}
	second := call("PUT", "/api/stacks/demo/draft", `{"base":"","files":{"compose.yaml":"services: {web: {image: nginx}}"},"version":1}`)
	if second.Code != 200 {
		t.Fatalf("update: %d %s", second.Code, second.Body)
	}
	if w := call("DELETE", "/api/stacks/demo/draft", `{"version":1}`); w.Code != 409 {
		t.Fatalf("stale discard: %d", w.Code)
	}
	if w := call("PUT", "/api/stacks/demo/draft", `{"base":"","files":{".env":"PASSWORD=secret"},"version":2}`); w.Code != 422 {
		t.Fatalf("plaintext dotenv: %d", w.Code)
	}
	var version int64
	if e := pool.QueryRow(ctx, "SELECT version FROM stack_drafts WHERE stack_id='demo' AND user_id=$1", id).Scan(&version); e != nil || version != 2 {
		t.Fatalf("stored draft: %d %v", version, e)
	}
	if w := call("DELETE", "/api/stacks/demo/draft", `{"version":2}`); w.Code != 200 {
		t.Fatalf("discard: %d %s", w.Code, w.Body)
	}
}
