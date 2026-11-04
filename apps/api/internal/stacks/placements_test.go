package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/internal/placement"
	"github.com/deranjer/vigara/internal/stack"
	"github.com/deranjer/vigara/internal/stackgit"
)

func TestPlacementsRequireAdministrator(t *testing.T) {
	for _, role := range []string{auth.RoleViewer, auth.RoleOperator} {
		mux := http.NewServeMux()
		Register(mux, Config{Authz: authorizer{auth.User{Role: role}}})
		for _, method := range []string{"GET", "PUT"} {
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(method, "/api/stack-placements", strings.NewReader(`{}`)))
			if w.Code != 403 {
				t.Fatalf("%s %s: %d", role, method, w.Code)
			}
		}
	}
}

func TestDirectDeploymentChecksCurrentAssignmentIntent(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@localhost", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@localhost")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("fixture git: %v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	remote := filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	git(root, "init", "--bare", remote)
	git(root, "init", "-b", "main", seed)
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("Fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(seed, "add", "README.md")
	git(seed, "commit", "-m", "Initialize")
	git(seed, "push", remote, "main")
	repo := &stackgit.Repository{Directory: filepath.Join(root, "cache.git"), Remote: remote, Branch: "main"}
	s := stack.Snapshot{ID: "web", Files: map[string]string{"vigara-stack.yaml": "apiVersion: vigara.io/v1\nkind: Stack\nstackId: web\ndisplayName: Web\nprojectName: web\ncompose:\n  files: [compose.yaml]\n  projectDirectory: .\n  interpolationFiles: []\nconfigurationRevisionPolicy: sha256-git-tree-and-placement-inputs-v1\n", "compose.yaml": "services:\n  app:\n    image: nginx\n"}}
	old, err := repo.Save(ctx, s, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err = directPlacement(ctx, repo, "web"); err != nil {
		t.Fatal(err)
	}
	p, err := repo.Placements(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	p.Files["profiles/north/vigara-profile.yaml"] = "apiVersion: vigara.io/v1\nkind: ServerProfile\nprofileId: north\ndisplayName: North\nrequirements: {}\n"
	p.Files["assignments/north/web.yaml"] = "apiVersion: vigara.io/v1\nkind: DeploymentAssignment\nassignmentId: web-north\nstackId: web\nprofileId: north\nenvironment: production\nownership:\n  mode: exclusive\n  ownershipEpoch: 1\nenabled: true\n"
	if _, err = repo.SavePlacements(ctx, p, "external-writer"); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.Read(ctx, "web", old.Commit); err != nil {
		t.Fatal("old stack revision should remain readable", err)
	}
	if err = directPlacement(ctx, repo, "web"); !errors.Is(err, errPlacementInactive) {
		t.Fatalf("current enabled assignment ignored: %v", err)
	}
	if err = directPlacement(ctx, repo, "other"); err != nil {
		t.Fatal("unrelated stack blocked", err)
	}
	p, err = repo.Placements(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	p.Files["assignments/north/web.yaml"] = strings.Replace(p.Files["assignments/north/web.yaml"], "enabled: true", "enabled: false", 1)
	if _, err = repo.SavePlacements(ctx, p, "test"); err != nil {
		t.Fatal(err)
	}
	if err = directPlacement(ctx, repo, "web"); err != nil {
		t.Fatal("disabled preparation blocked direct deployment", err)
	}
	git(seed, "pull", remote, "main")
	if err = os.WriteFile(filepath.Join(seed, "profiles", "north", "vigara-profile.yaml"), []byte("kind: Invalid\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(seed, "add", "profiles/north/vigara-profile.yaml")
	git(seed, "commit", "-m", "Invalid external profile")
	git(seed, "push", remote, "main")
	if err = directPlacement(ctx, repo, "web"); !errors.Is(err, placement.ErrInvalid) {
		t.Fatalf("invalid remote manifest ignored: %v", err)
	}
}

func TestPlacementsCannotActivateThroughManifestEditor(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux, Config{Authz: authorizer{auth.User{Role: auth.RoleAdmin}}})
	p := stackgit.Placements{Files: map[string]string{"assignments/north/web.yaml": "apiVersion: vigara.io/v1\nkind: DeploymentAssignment\nassignmentId: web-north\nstackId: web\nprofileId: north\nenvironment: production\nownership:\n  mode: exclusive\n  ownershipEpoch: 1\nenabled: true\n"}}
	b, _ := json.Marshal(p)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("PUT", "/api/stack-placements", strings.NewReader(string(b))))
	if w.Code != 422 || !strings.Contains(w.Body.String(), "enabled: false") {
		t.Fatalf("activation: %d %s", w.Code, w.Body)
	}
}
