package stacks

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/internal/stack"
	"github.com/deranjer/vigara/internal/stackgit"
)

func TestProfileBindingActivationAndCapturedDeployment(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	root := t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@localhost", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@localhost")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %v %s", err, b)
		}
	}
	remote := filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	git(root, "init", "--bare", remote)
	git(root, "init", "-b", "main", seed)
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("Fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	git(seed, "add", "README.md")
	git(seed, "commit", "-m", "Initialize")
	git(seed, "push", remote, "main")
	repo := &stackgit.Repository{Directory: filepath.Join(root, "cache.git"), Remote: remote, Branch: "main"}
	s := stack.Snapshot{ID: "demo", Files: map[string]string{"vigara-stack.yaml": "apiVersion: vigara.io/v1\nkind: Stack\nstackId: demo\ndisplayName: Demo\nprojectName: demo\ncompose:\n  files: [compose.yaml]\n  projectDirectory: .\n  interpolationFiles: []\nconfigurationRevisionPolicy: sha256-git-tree-and-placement-inputs-v1\n", "compose.yaml": "services:\n  web:\n    image: nginx\n"}}
	old, err := repo.Save(ctx, s, "test")
	if err != nil {
		t.Fatal(err)
	}
	var user, agent, other string
	if err = pool.QueryRow(ctx, "INSERT INTO users(username,display_name,password_hash) VALUES('owner','Owner','test') RETURNING id::text").Scan(&user); err != nil {
		t.Fatal(err)
	}
	for i, out := range []*string{&agent, &other} {
		var device string
		if err = pool.QueryRow(ctx, "INSERT INTO devices(display_name) VALUES('Test') RETURNING id::text").Scan(&device); err != nil {
			t.Fatal(err)
		}
		if err = pool.QueryRow(ctx, "INSERT INTO agent_identities(device_id,credential_hash,protocol_version) VALUES($1,$2,'v1') RETURNING id::text", device, []byte{byte(i + 1)}).Scan(out); err != nil {
			t.Fatal(err)
		}
	}
	hub := &fakeAgent{}
	mux := http.NewServeMux()
	api := Register(mux, Config{Pool: pool, Repository: repo, Hub: hub, Targets: map[string]Binding{"demo": {AgentID: agent, ProjectName: "demo"}}, Authz: authorizer{auth.User{ID: user, Username: "owner", Role: auth.RoleAdmin}}})
	call := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(string(raw))))
		return w
	}
	p, err := repo.Placements(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	p.Files["profiles/north/vigara-profile.yaml"] = "apiVersion: vigara.io/v1\nkind: ServerProfile\nprofileId: north\ndisplayName: North\nrequirements: {}\n"
	p.Files["assignments/north/demo.yaml"] = "apiVersion: vigara.io/v1\nkind: DeploymentAssignment\nassignmentId: demo-north\nstackId: demo\nprofileId: north\nenvironment: production\nownership:\n  mode: exclusive\n  ownershipEpoch: 1\nenabled: false\n"
	saved, err := repo.SavePlacements(ctx, p, "test")
	if err != nil {
		t.Fatal(err)
	}
	bind := func(target string) *httptest.ResponseRecorder {
		return call("POST", "/api/stack-placements/bindings", map[string]string{"profileId": "north", "agentId": target, "commit": saved.Commit})
	}
	if w := bind(other); w.Code != 409 {
		t.Fatalf("wrong initial host accepted: %d %s", w.Code, w.Body)
	}
	if w := bind(agent); w.Code != 200 {
		t.Fatalf("binding %d %s", w.Code, w.Body)
	}
	if w := bind(other); w.Code != 409 {
		t.Fatalf("ownership transfer accepted: %d", w.Code)
	}
	p, err = repo.Placements(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	p.Files["assignments/north/demo.yaml"] = strings.Replace(p.Files["assignments/north/demo.yaml"], "enabled: false", "enabled: true", 1)
	w := call("PUT", "/api/stack-placements", p)
	if w.Code != 200 {
		t.Fatalf("activate %d %s", w.Code, w.Body)
	}
	var active stackgit.Saved
	if err = json.Unmarshal(w.Body.Bytes(), &active); err != nil {
		t.Fatal(err)
	}
	if w := call("POST", "/api/stacks/demo/plan", map[string]string{"commit": old.Commit}); w.Code != 409 {
		t.Fatalf("historical bypass %d %s", w.Code, w.Body)
	}
	if w := call("POST", "/api/stacks/demo/plan", map[string]string{"commit": active.Commit}); w.Code != 200 {
		t.Fatalf("preview %d %s", w.Code, w.Body)
	}
	if w := call("POST", "/api/stacks/demo/deploy", map[string]string{"commit": active.Commit, "token": strings.Repeat("e", 64)}); w.Code != 409 {
		t.Fatal("unrecorded preview token accepted")
	}
	if w := call("POST", "/api/stacks/demo/deploy", map[string]string{"commit": active.Commit, "token": strings.Repeat("d", 64)}); w.Code != 202 {
		t.Fatalf("submit %d %s", w.Code, w.Body)
	}
	api.tick(ctx)
	if len(hub.deployed) != 1 || hub.deployed[0].Job.Snapshot.Placement == nil || hub.deployed[0].Job.Snapshot.Placement.ProfileID != "north" {
		t.Fatal("job lost placement")
	}
	var status string
	var result []byte
	if err = pool.QueryRow(ctx, "SELECT status,result FROM stack_deployments").Scan(&status, &result); err != nil || status != "succeeded" || !strings.Contains(string(result), "sha256:") {
		t.Fatalf("release result %s %s %v", status, result, err)
	}
	// A queued job cannot silently ignore an external profile edit.
	if w := call("POST", "/api/stacks/demo/plan", map[string]string{"commit": active.Commit}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := call("POST", "/api/stacks/demo/deploy", map[string]string{"commit": active.Commit, "token": strings.Repeat("d", 64)}); w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	p, err = repo.Placements(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	p.Files["profiles/north/vigara-profile.yaml"] += "# changed by external writer\n"
	if _, err = repo.SavePlacements(ctx, p, "external"); err != nil {
		t.Fatal(err)
	}
	api.tick(ctx)
	if len(hub.deployed) != 1 {
		t.Fatal("stale profile job reached agent")
	}
}
