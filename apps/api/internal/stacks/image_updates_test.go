package stacks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/internal/imageupdates"
	"github.com/deranjer/vigara/internal/protocol"
	"github.com/deranjer/vigara/internal/stack"
	"github.com/deranjer/vigara/internal/stackgit"
)

type updateAgent struct {
	fakeAgent
	checks          map[string]protocol.DockerImageCheck
	containers      []protocol.DockerContainerInfo
	requestedChecks []protocol.DockerImageCheckParams
}

func (f *updateAgent) Call(ctx context.Context, agent, method string, params, out any) error {
	switch method {
	case protocol.MethodComposeImagePlan:
		raw, _ := json.Marshal(params)
		var q struct{ Images map[string]string }
		_ = json.Unmarshal(raw, &q)
		*out.(*stack.Plan) = stack.Plan{Token: strings.Repeat("f", 64), Services: []string{"web", "db"}, Images: q.Images, ProjectName: "demo"}
		return nil
	case protocol.MethodDockerList:
		*out.(*protocol.DockerListResult) = protocol.DockerListResult{Containers: []protocol.DockerContainerInfo{{ID: "web", Name: "web", Project: "demo", Service: "web"}, {ID: "db", Name: "db", Project: "demo", Service: "db"}}}
		if f.containers != nil {
			out.(*protocol.DockerListResult).Containers = f.containers
		}
		return nil
	case protocol.MethodDockerImageCheck:
		p := params.(protocol.DockerImageCheckParams)
		f.requestedChecks = append(f.requestedChecks, p)
		*out.(*protocol.DockerImageCheck) = f.checks[p.ID]
		if p.Ref != "" {
			out.(*protocol.DockerImageCheck).Image = p.Ref
		}
		return nil
	}
	return f.fakeAgent.Call(ctx, agent, method, params, out)
}
func imageFixture(t *testing.T) (*API, *http.ServeMux, stack.Snapshot, string, *updateAgent) {
	t.Helper()
	ctx := context.Background()
	pool := testPool(t)
	var user, device, agent string
	for _, q := range []struct {
		sql string
		out *string
	}{
		{"INSERT INTO users(username,display_name,password_hash) VALUES('image-owner','Owner','test') RETURNING id::text", &user},
		{"INSERT INTO devices(display_name) VALUES('Image host') RETURNING id::text", &device},
	} {
		if err := pool.QueryRow(ctx, q.sql).Scan(q.out); err != nil {
			t.Fatal(err)
		}
	}
	if err := pool.QueryRow(ctx, "INSERT INTO agent_identities(device_id,credential_hash,protocol_version) VALUES($1,$2,'v1') RETURNING id::text", device, []byte("image-test")).Scan(&agent); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@localhost", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@localhost")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, b)
		}
	}
	git(root, "init", "--bare", remote)
	git(root, "init", "-b", "main", seed)
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	git(seed, "add", "README.md")
	git(seed, "commit", "-m", "fixture")
	git(seed, "push", remote, "main")
	repo := &stackgit.Repository{Directory: filepath.Join(root, "cache.git"), Remote: remote, Branch: "main"}
	s := stack.Snapshot{ID: "demo", Files: map[string]string{"vigara-stack.yaml": "apiVersion: vigara.io/v1\nkind: Stack\nstackId: demo\ndisplayName: Demo\nprojectName: demo\ncompose:\n  files: [compose.yaml]\n  projectDirectory: .\n  interpolationFiles: []\nconfigurationRevisionPolicy: sha256-git-tree-and-placement-inputs-v1\n", "compose.yaml": "services:\n  web:\n    image: nginx:stable\n  db:\n    image: postgres:17\n"}}
	saved, err := repo.Save(ctx, s, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	s.Commit = saved.Commit
	if _, err = pool.Exec(ctx, "INSERT INTO stack_bindings(stack_id,agent_id,project_name) VALUES('demo',$1,'demo')", agent); err != nil {
		t.Fatal(err)
	}
	images, _ := json.Marshal(map[string]string{"web": "nginx@sha256:" + strings.Repeat("a", 64), "db": "postgres@sha256:" + strings.Repeat("a", 64)})
	tracking := []byte(`{"web":"nginx:stable","db":"postgres:17"}`)
	raw, _ := json.Marshal(s)
	_, err = pool.Exec(ctx, `INSERT INTO stack_deployments(id,stack_id,agent_id,project_name,actor_name,commit_id,configuration_digest,snapshot,plan_token,pull,status,images,tracking) VALUES($1,'demo',$2,'demo','fixture',$3,$4,$5,$6,true,'succeeded',$7,$8)`, strings.Repeat("a", 64), agent, s.Commit, stack.Digest(s), raw, strings.Repeat("d", 64), images, tracking)
	if err != nil {
		t.Fatal(err)
	}
	hub := &updateAgent{checks: map[string]protocol.DockerImageCheck{}}
	for _, service := range []string{"web", "db"} {
		ref := "nginx:stable"
		if service == "db" {
			ref = "postgres:17"
		}
		hub.checks[service] = protocol.DockerImageCheck{ID: service, Name: service, Project: "demo", Service: service, Image: ref, Current: "sha256:" + strings.Repeat("a", 64), Available: "sha256:" + strings.Repeat("b", 64), Manifest: "sha256:" + strings.Repeat("c", 64), Status: "available"}
	}
	mux := http.NewServeMux()
	api := Register(mux, Config{Pool: pool, Repository: repo, Hub: hub, Targets: map[string]Binding{"demo": {AgentID: agent, ProjectName: "demo"}}, Authz: authorizer{auth.User{ID: user, Username: "image-owner", Role: auth.RoleAdmin}}})
	return api, mux, s, agent, hub
}
func imageCall(mux *http.ServeMux, method, path string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(string(b))))
	return w
}
func openImageWindow() imageupdates.Window {
	return imageupdates.Window{Days: []int{0, 1, 2, 3, 4, 5, 6}, Start: "00:00", Minutes: 1440, Timezone: "UTC"}
}

func TestImageUpdatesRequireOptInAndPreserveExcludedService(t *testing.T) {
	a, mux, s, agent, hub := imageFixture(t)
	ctx := context.Background()
	settings := imageSettings{Interval: 360, Maintenance: openImageWindow()}
	for _, r := range hub.checks {
		if err := a.storeImageCheck(ctx, agent, r); err != nil {
			t.Fatal(err)
		}
	}
	a.scheduleImageUpdate(ctx, settings)
	var count int
	_ = a.Pool.QueryRow(ctx, "SELECT count(*) FROM stack_deployments WHERE automatic").Scan(&count)
	if count != 0 {
		t.Fatal("monitoring enabled automatic deployment")
	}
	w := imageCall(mux, "GET", "/api/stacks/demo/update-policy", nil)
	var p updatePolicy
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	if p.Enabled || p.Version != 0 || len(p.Services) != 2 {
		t.Fatal(w.Body.String())
	}
	p.Enabled = true
	p.Excluded = []string{"db"}
	window := openImageWindow()
	p.Maintenance = &window
	w = imageCall(mux, "PUT", "/api/stacks/demo/update-policy", p)
	if w.Code != 200 {
		t.Fatalf("opt-in %d %s", w.Code, w.Body)
	}
	if err := a.queueImageUpdate(ctx, "demo", settings); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	var automatic bool
	if err := a.Pool.QueryRow(ctx, "SELECT images,automatic FROM stack_deployments WHERE status='queued'").Scan(&raw, &automatic); err != nil {
		t.Fatal(err)
	}
	images := map[string]string{}
	_ = json.Unmarshal(raw, &images)
	if !automatic || images["web"] != "nginx@sha256:"+strings.Repeat("c", 64) || images["db"] != "postgres@sha256:"+strings.Repeat("a", 64) {
		t.Fatal("wrong image set", images)
	}
	a.scheduleImageUpdate(ctx, settings)
	_ = a.Pool.QueryRow(ctx, "SELECT count(*) FROM stack_deployments WHERE automatic").Scan(&count)
	if count != 1 {
		t.Fatal("duplicate automatic jobs", count)
	}
	stored, _ := a.updatePolicy(ctx, s.ID)
	if !a.automaticImageAllowed(ctx, s, &stored.Version) {
		t.Fatal("valid open-window policy refused")
	}
	a.tick(ctx)
	if len(hub.deployed) != 1 {
		t.Fatal("automatic job was not dispatched")
	}
	var status string
	_ = a.Pool.QueryRow(ctx, "SELECT status FROM stack_deployments WHERE automatic").Scan(&status)
	if status != "succeeded" {
		t.Fatal(status)
	}
}
func TestDisablingPolicyCancelsQueuedJobAndStaleEditsFail(t *testing.T) {
	a, mux, _, agent, hub := imageFixture(t)
	ctx := context.Background()
	for _, r := range hub.checks {
		if err := a.storeImageCheck(ctx, agent, r); err != nil {
			t.Fatal(err)
		}
	}
	w := openImageWindow()
	p := updatePolicy{Enabled: true, Maintenance: &w}
	response := imageCall(mux, "PUT", "/api/stacks/demo/update-policy", p)
	if response.Code != 200 {
		t.Fatal(response.Body)
	}
	_ = json.Unmarshal(response.Body.Bytes(), &p)
	a.scheduleImageUpdate(ctx, imageSettings{Interval: 360, Maintenance: w})
	stale := p
	p.Enabled = false
	response = imageCall(mux, "PUT", "/api/stacks/demo/update-policy", p)
	if response.Code != 200 {
		t.Fatal(response.Body)
	}
	if response = imageCall(mux, "PUT", "/api/stacks/demo/update-policy", stale); response.Code != 409 {
		t.Fatalf("stale policy accepted %d %s", response.Code, response.Body)
	}
	a.tick(ctx)
	if len(hub.deployed) != 0 {
		t.Fatal("disabled policy dispatched")
	}
}
func TestImageCheckNoticesAreDurableAndErrorsKeepLastSuccess(t *testing.T) {
	a, _, _, agent, hub := imageFixture(t)
	ctx := context.Background()
	r := hub.checks["web"]
	for range 2 {
		if err := a.storeImageCheck(ctx, agent, r); err != nil {
			t.Fatal(err)
		}
	}
	// A newer image is recorded once as a notice and raises no alert.
	var count int
	if err := a.Pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM image_update_notices)*10+(SELECT count(*) FROM alert_events)").Scan(&count); err != nil || count != 10 {
		t.Fatal("image notices*10+alert events", count, err)
	}
	// Under a fixed version tag a changed image is unexpected and alerts, once.
	pinned := r
	pinned.RunningImage = "example/web:1.2.3"
	pinned.Available = "sha256:" + strings.Repeat("e", 64)
	for range 2 {
		if err := a.storeImageCheck(ctx, agent, pinned); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Pool.QueryRow(ctx, "SELECT count(*) FROM alert_events e JOIN alert_incidents i ON i.id=e.incident_id WHERE i.rule_id='"+pinnedImageRule+"'").Scan(&count); err != nil || count != 1 {
		t.Fatal("pinned image alerts", count, err)
	}
	r.Status = "unknown"
	r.Message = "registry unavailable"
	if err := a.storeImageCheck(ctx, agent, r); err != nil {
		t.Fatal(err)
	}
	var status, previous string
	var last time.Time
	if err := a.Pool.QueryRow(ctx, "SELECT result->>'status',last_success->>'status',last_success_at FROM container_image_updates").Scan(&status, &previous, &last); err != nil || status != "unknown" || previous != "available" {
		t.Fatal(status, previous, last, err)
	}
}
func TestUpdatePolicyPermissions(t *testing.T) {
	for _, role := range []string{auth.RoleViewer, auth.RoleOperator} {
		mux := http.NewServeMux()
		Register(mux, Config{Authz: authorizer{auth.User{Role: role}}})
		for _, path := range []string{"/api/image-updates/settings", "/api/stacks/demo/update-policy"} {
			response := imageCall(mux, "PUT", path, map[string]bool{"enabled": true})
			if response.Code != 403 {
				t.Fatal(fmt.Sprintf("%s %s %d", role, path, response.Code))
			}
		}
	}
}

func TestFailedAutomaticReleasePausesAndNotifiesOnce(t *testing.T) {
	a, mux, _, agent, hub := imageFixture(t)
	ctx := context.Background()
	window := openImageWindow()
	for _, r := range hub.checks {
		if err := a.storeImageCheck(ctx, agent, r); err != nil {
			t.Fatal(err)
		}
	}
	response := imageCall(mux, "PUT", "/api/stacks/demo/update-policy", updatePolicy{Enabled: true, Maintenance: &window})
	if response.Code != 200 {
		t.Fatal(response.Body)
	}
	settings := imageSettings{Interval: 360, Maintenance: window}
	if err := a.queueImageUpdate(ctx, "demo", settings); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Pool.Exec(ctx, "UPDATE stack_deployments SET status='failed',stage='apply',message='test failure' WHERE automatic"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		a.scheduleImageUpdate(ctx, settings)
	}
	p, err := a.updatePolicy(ctx, "demo")
	if err != nil || p.Paused == "" {
		t.Fatal("failed update did not pause", p, err)
	}
	var count int
	if err = a.Pool.QueryRow(ctx, "SELECT count(*) FROM alert_events WHERE payload->>'rule'='Automatic image update needs attention'").Scan(&count); err != nil || count != 1 {
		t.Fatal("failure notification missing or duplicated", count, err)
	}
}
