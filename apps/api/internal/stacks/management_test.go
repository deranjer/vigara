package stacks

import (
	"context"
	"encoding/json"
	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/internal/stack"
	"github.com/deranjer/vigara/internal/stackgit"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitRemoteValidationAndRedaction(t *testing.T) {
	for _, remote := range []string{"ssh://git@example.com/home/stacks.git", "git@example.com:home/stacks.git", "https://example.com/stacks.git"} {
		if !validRemote(remote, "") {
			t.Fatal("valid rejected", remote)
		}
	}
	for _, remote := range []string{"ext::sh -c evil", "-option", "file:///etc", "/arbitrary/private.git", "https://user:secret@example.com/repo", "https://example.com/repo?token=secret", "ssh://git:secret@example.com/repo", "https://example.com/repo\n"} {
		if validRemote(remote, "") {
			t.Fatal("unsafe remote accepted", remote)
		}
	}
	localRemote := filepath.Join(t.TempDir(), "remote.git")
	if !validRemote(localRemote, localRemote) {
		t.Fatal("existing local repository rejected")
	}
	if strings.Contains(publicRemote("https://user:secret@example.com/repo?token=secret"), "secret") {
		t.Fatal("credential leaked")
	}
	if publicRemote("git@example.com:repo.git") != "git@example.com:repo.git" {
		t.Fatal("SCP URL lost")
	}
}
func TestSettingsAndCreateStackPersistence(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@localhost", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@localhost")
		if out, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("git: %v %s", e, out)
		}
	}
	git(root, "init", "--bare", remote)
	git(root, "init", "-b", "main", seed)
	if e := os.WriteFile(filepath.Join(seed, "README.md"), []byte("Stacks"), 0600); e != nil {
		t.Fatal(e)
	}
	git(seed, "add", "README.md")
	git(seed, "commit", "-m", "Initialize")
	git(seed, "push", remote, "main")
	git(seed, "branch", "testing")
	git(seed, "push", remote, "testing")
	var user, device, agent string
	if e := pool.QueryRow(ctx, "INSERT INTO users(username,display_name,password_hash) VALUES('owner','Owner','test-only') RETURNING id::text").Scan(&user); e != nil {
		t.Fatal(e)
	}
	if e := pool.QueryRow(ctx, "INSERT INTO devices(display_name) VALUES('Living room') RETURNING id::text").Scan(&device); e != nil {
		t.Fatal(e)
	}
	if e := pool.QueryRow(ctx, "INSERT INTO agent_identities(device_id,credential_hash,protocol_version) VALUES($1,$2,'v1') RETURNING id::text", device, []byte("test")).Scan(&agent); e != nil {
		t.Fatal(e)
	}
	repo := &stackgit.Repository{Directory: filepath.Join(root, "cache.git"), Remote: remote, Branch: "main"}
	mux := http.NewServeMux()
	api := Register(mux, Config{Pool: pool, Repository: repo, Hub: &fakeAgent{}, Authz: authorizer{auth.User{ID: user, Username: "owner", Role: auth.RoleAdmin}}})
	call := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(string(raw))))
		return w
	}
	settings := gitSettings{Remote: remote, Branch: "testing", Version: 0}
	if w := call("POST", "/api/stacks/settings/test", settings); w.Code != 200 {
		t.Fatalf("test: %d %s", w.Code, w.Body)
	}
	s, e := api.settings(ctx)
	if e != nil || s.Version != 0 || s.Branch != "main" {
		t.Fatal("connection test mutated settings", s, e)
	}
	if w := call("PUT", "/api/stacks/settings", settings); w.Code != 200 {
		t.Fatalf("settings: %d %s", w.Code, w.Body)
	}
	restarted := Register(http.NewServeMux(), Config{Pool: pool, Repository: repo})
	current, e := restarted.repository(ctx)
	if e != nil || current.Branch != "testing" {
		t.Fatal("settings did not survive restart", e)
	}
	if w := call("PUT", "/api/stacks/settings", settings); w.Code != 409 {
		t.Fatal("stale settings accepted", w.Code)
	}
	if w := call("GET", "/api/stacks/setup", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "/srv/managed") {
		t.Fatal("setup", w.Code, w.Body)
	}
	body := map[string]string{"id": "media", "name": "Media server", "agentId": agent, "projectName": "media"}
	if w := call("POST", "/api/stacks", body); w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	if w := call("POST", "/api/stacks", body); w.Code != 409 {
		t.Fatal("duplicate ID accepted", w.Code)
	}
	if w := call("GET", "/api/stacks/media", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "Media server") || !strings.Contains(w.Body.String(), "\"assigned\":true") {
		t.Fatal("draft detail", w.Code, w.Body)
	}
	var raw []byte
	if e := pool.QueryRow(ctx, "SELECT files FROM stack_drafts WHERE stack_id='media'").Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var files map[string]string
	_ = json.Unmarshal(raw, &files)
	if _, e := stack.Validate(stack.Snapshot{ID: "media", Files: files}); e != nil {
		t.Fatal("invalid starter", e, files)
	}
	if w := call("GET", "/api/stacks", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "Living room") || !strings.Contains(w.Body.String(), "Media server") {
		t.Fatal("list draft", w.Code, w.Body)
	}
	settings.Version = 1
	settings.Branch = "main"
	if w := call("PUT", "/api/stacks/settings", settings); w.Code != 409 {
		t.Fatal("repository changed despite drafts", w.Code, w.Body)
	}
	var count int
	if e := pool.QueryRow(ctx, "SELECT count(*) FROM stack_deployments").Scan(&count); e != nil || count != 0 {
		t.Fatal("creation deployed", count, e)
	}
	if _, e := repo.Read(ctx, "media", ""); !errorsIsNotFound(e) {
		t.Fatal("creation committed", e)
	}
	if w := call("POST", "/api/stacks/media/save", map[string]int{"version": 1}); w.Code != 200 {
		t.Fatal("new stack save failed", w.Code, w.Body)
	}
	configured, e := api.repository(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if saved, e := configured.Read(ctx, "media", ""); e != nil || !strings.Contains(saved.Files["vigara-stack.yaml"], "Media server") {
		t.Fatal("new stack was not saved to configured branch", e)
	}
	if w := call("GET", "/api/stacks", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "\"serviceCount\":1") {
		t.Fatal("saved service count", w.Code, w.Body)
	}

}
func errorsIsNotFound(e error) bool { return e == stackgit.ErrNotFound }
func TestManagementRequiresAdmin(t *testing.T) {
	for _, role := range []string{auth.RoleViewer, auth.RoleOperator} {
		mux := http.NewServeMux()
		Register(mux, Config{Authz: authorizer{auth.User{Role: role}}})
		for _, endpoint := range []struct{ method, path string }{{"GET", "/api/stacks/settings"}, {"PUT", "/api/stacks/settings"}, {"POST", "/api/stacks/settings/test"}, {"GET", "/api/stacks/setup"}, {"POST", "/api/stacks"}} {
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(endpoint.method, endpoint.path, strings.NewReader("{}")))
			if w.Code != 403 {
				t.Fatal("management access allowed", role, endpoint, w.Code)
			}
		}
	}
}
