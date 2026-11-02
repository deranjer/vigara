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

	"github.com/deranjer/vigara/apps/api/internal/audit"
	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/internal/composesecrets"
	"github.com/deranjer/vigara/internal/protocol"
	"github.com/deranjer/vigara/internal/stack"
	"github.com/deranjer/vigara/internal/stackgit"
	"gopkg.in/yaml.v3"
)

type importAgent struct {
	key     string
	calls   []string
	changed bool
	result  stack.Result
}

func (f *importAgent) Call(ctx context.Context, agent, method string, input, out any) error {
	f.calls = append(f.calls, method)
	switch method {
	case protocol.MethodComposeDiscover:
		*out.(*protocol.ComposeDiscovery) = protocol.ComposeDiscovery{Root: "/srv/stacks", Candidates: []protocol.ComposeImportCandidate{{Directory: "vigara", ProjectName: "vigara", Token: strings.Repeat("a", 64), State: "running", Containers: 2, ComposeFiles: []string{"vigara/compose.yml"}, EnvironmentFiles: []string{"vigara/.env"}, Issues: []string{}}}}
	case protocol.MethodComposeImportPreview:
		*out.(*protocol.ComposeImportPreview) = protocol.ComposeImportPreview{Candidate: protocol.ComposeImportCandidate{Directory: "vigara", ProjectName: "vigara", Token: strings.Repeat("a", 64)}, Files: map[string]string{"vigara/compose.yml": "services:\n  app:\n    image: example/vigara:latest\n    env_file: .env\n"}}
	case protocol.MethodComposeImportPrepare:
		q := input.(protocol.ComposeImportRequest)
		ciphertext, err := composesecrets.EncryptAgeDotenv(ctx, []byte("PASSWORD=existing-import-secret\n"), []string{q.EditorRecipient, f.key, q.RecoveryRecipient})
		if err != nil {
			return err
		}
		m := stack.Manifest{APIVersion: "vigara.io/v1", Kind: "Stack", StackID: q.ID, DisplayName: q.Name, ProjectName: "vigara", ConfigurationRevisionPolicy: "sha256-git-tree-and-placement-inputs-v1"}
		m.Compose.Files = []string{"compose.yml"}
		m.Compose.ProjectDirectory = "."
		m.Compose.InterpolationFiles = []stack.Input{{Path: "secrets/runtime.env.age", Purpose: "stack", Sensitivity: "encrypted", Format: composesecrets.AgeFormat}}
		m.Compose.EnvironmentFiles = []stack.Input{{Path: "secrets/runtime.env.age", Target: ".env", Purpose: "deployment", Sensitivity: "encrypted", Format: composesecrets.AgeFormat}}
		m.SecretRecipientRefs = []stack.RecipientRef{{Class: "central-editor", KeyRef: "import-editor"}, {Class: "target-agent", KeyRef: "import-agent"}, {Class: "operator-recovery", KeyRef: "import-recovery"}}
		raw, _ := yaml.Marshal(m)
		*out.(*protocol.ComposeImportPrepared) = protocol.ComposeImportPrepared{Target: protocol.ComposeTargetInfo{ID: q.ID, ProjectName: "vigara", Directory: "/srv/stacks/vigara"}, AgentRecipient: f.key, Files: map[string]string{"vigara-stack.yaml": string(raw), "compose.yml": "services:\n  app:\n    image: example/vigara:latest\n    env_file: .env\n", "secrets/runtime.env.age": string(ciphertext)}}
	case protocol.MethodComposeImportConfirm:
		if f.changed {
			return fmt.Errorf("runtime changed")
		}
		q := input.(protocol.ComposeImportRequest)
		*out.(*protocol.ComposeTargetInfo) = protocol.ComposeTargetInfo{ID: q.ID, ProjectName: "vigara", Directory: "/srv/stacks/vigara"}
	case protocol.MethodComposeStatus:
		*out.(*stack.Result) = f.result
	case protocol.MethodComposeValidate:
		*out.(*stack.Plan) = stack.Plan{ProjectName: "vigara"}
	default:
		return fmt.Errorf("unexpected import method: %s", method)
	}
	return nil
}

func importRepository(t *testing.T) (*stackgit.Repository, string) {
	t.Helper()
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@localhost", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@localhost")
		if data, err := c.CombinedOutput(); err != nil {
			t.Fatalf("fixture git: %v %s", err, data)
		}
	}
	remote := filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	git("init", "--bare", remote)
	git("init", "-b", "main", seed)
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("Existing stacks\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("-C", seed, "add", "README.md")
	git("-C", seed, "commit", "-m", "Initial")
	git("-C", seed, "push", remote, "main")
	return &stackgit.Repository{Directory: filepath.Join(root, "cache.git"), Remote: remote, Branch: "main"}, remote
}

func TestImportExistingStackPersistsEncryptedGitAndBindingWithoutDeploy(t *testing.T) {
	testImportFlow(t, false)
}

func TestImportRejectedPushKeepsRecoverableEncryptedDraft(t *testing.T) { testImportFlow(t, true) }

func testImportFlow(t *testing.T, rejectPush bool) {
	pool := testPool(t)
	ctx := context.Background()
	repo, remote := importRepository(t)
	var user, device, agent string
	if err := pool.QueryRow(ctx, "INSERT INTO users(username,display_name,password_hash) VALUES('importer','Importer','test-only') RETURNING id::text").Scan(&user); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "INSERT INTO devices(display_name) VALUES('server') RETURNING id::text").Scan(&device); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "INSERT INTO agent_identities(device_id,credential_hash,protocol_version) VALUES($1,decode('aabb','hex'),'v1') RETURNING id::text", device).Scan(&agent); err != nil {
		t.Fatal(err)
	}
	key, err := composesecrets.EnsureAgeIdentity(filepath.Join(t.TempDir(), "agent.agekey"))
	if err != nil {
		t.Fatal(err)
	}
	hub := &importAgent{key: key}
	mux := http.NewServeMux()
	api := Register(mux, Config{Repository: repo, Pool: pool, Hub: hub, Authz: authorizer{auth.User{ID: user, Username: "importer", Role: auth.RoleAdmin}}, Audit: audit.NewRecorder(pool)})
	call := func(path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		data, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", path, strings.NewReader(string(data)))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	q := importRequest{AgentID: agent, Directory: "vigara", Token: strings.Repeat("a", 64), ID: "vigara", Name: "Vigara"}
	if w := call("/api/stacks/imports", q); w.Code != 422 || len(hub.calls) != 0 {
		t.Fatal("unreviewed import proceeded", w.Code)
	}
	if w := call("/api/stacks/imports/preview", q); w.Code != 200 || strings.Contains(w.Body.String(), "existing-import-secret") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("unsafe preview", w.Code, w.Body)
	}
	recovery := httptest.NewRecorder()
	mux.ServeHTTP(recovery, httptest.NewRequest("GET", "/api/stacks/imports/recovery", nil))
	if recovery.Code != 200 || !strings.Contains(recovery.Body.String(), "AGE-SECRET-KEY-") || recovery.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("recovery download failed", recovery.Code)
	}
	q.Reviewed = true
	q.RecoverySaved = true
	hub.changed = true
	if w := call("/api/stacks/imports", q); w.Code != 409 {
		t.Fatal("changed runtime imported", w.Code, w.Body)
	}
	var count int
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM stack_bindings").Scan(&count); err != nil || count != 0 {
		t.Fatal("failed confirmation persisted binding", count, err)
	}
	hub.changed = false
	if rejectPush {
		if err = os.WriteFile(filepath.Join(remote, "hooks", "pre-receive"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	w := call("/api/stacks/imports", q)
	expected := 201
	if rejectPush {
		expected = 202
	}
	if w.Code != expected {
		t.Fatal("import", w.Code, w.Body)
	}
	if rejectPush {
		if err = pool.QueryRow(ctx, "SELECT count(*) FROM stack_drafts WHERE stack_id='vigara'").Scan(&count); err != nil || count != 1 {
			t.Fatal("rejected push lost encrypted draft", count, err)
		}
		detail := httptest.NewRecorder()
		mux.ServeHTTP(detail, httptest.NewRequest("GET", "/api/stacks/vigara", nil))
		if detail.Code != 200 || strings.Contains(detail.Body.String(), "existing-import-secret") {
			t.Fatal("registered draft cannot be recovered", detail.Code)
		}
		if err = os.Remove(filepath.Join(remote, "hooks", "pre-receive")); err != nil {
			t.Fatal(err)
		}
		if w = call("/api/stacks/vigara/save", map[string]int{"version": 1}); w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"saved"`) {
			t.Fatal("retry save failed", w.Code, w.Body)
		}
	}
	snapshot, err := repo.Read(ctx, "vigara", "")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(snapshot.Files)
	if strings.Contains(string(encoded), "existing-import-secret") || strings.Contains(string(encoded), "AGE-SECRET-KEY-") {
		t.Fatal("secret entered Git")
	}
	binding, ok := api.lookupTarget(ctx, "vigara")
	if !ok || !binding.Imported || len(binding.Recipients) != 3 || binding.Directory != "/srv/stacks/vigara" {
		t.Fatal("binding lost", binding)
	}
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM stack_drafts WHERE stack_id='vigara'").Scan(&count); err != nil || count != 0 {
		t.Fatal("saved import left draft", count, err)
	}
	if w = call("/api/stacks/imports/discover", importRequest{AgentID: agent}); w.Code != 200 || !strings.Contains(w.Body.String(), `"imported":true`) {
		t.Fatal("discovery didn't identify registered project", w.Code, w.Body)
	}
	if w = call("/api/stacks/imports", q); w.Code != 409 {
		t.Fatal("duplicate import accepted", w.Code)
	}
	for _, method := range hub.calls {
		if method == protocol.MethodComposeDeploy || method == protocol.MethodComposeCreate || (!rejectPush && method == protocol.MethodComposeValidate) {
			t.Fatal("import mutated runtime", method)
		}
	}
	// Lost self-update results are read from the host journal after restart.
	jobID := strings.Repeat("d", 64)
	token := strings.Repeat("e", 64)
	images := map[string]string{"app": "example/vigara@sha256:" + strings.Repeat("f", 64)}
	snapshotRaw, _ := json.Marshal(snapshot)
	imagesRaw, _ := json.Marshal(images)
	_, err = pool.Exec(ctx, `INSERT INTO stack_deployments(id,stack_id,agent_id,project_name,actor_id,actor_name,commit_id,configuration_digest,snapshot,plan_token,pull,status,images) VALUES($1,'vigara',$2,'vigara',$3,'importer',$4,$5,$6,$7,false,'unknown',$8)`, jobID, agent, user, snapshot.Commit, stack.Digest(snapshot), snapshotRaw, token, imagesRaw)
	if err != nil {
		t.Fatal(err)
	}
	hub.result = stack.Result{Status: "succeeded", Stage: "complete", JobID: jobID, Commit: strings.Repeat("0", 40), ConfigurationRevision: stack.ConfigurationRevision(snapshot), PlanToken: token, Images: images, Readiness: "compose-wait-passed"}
	api.tick(ctx)
	var status string
	if err = pool.QueryRow(ctx, "SELECT status FROM stack_deployments WHERE id=$1", jobID).Scan(&status); err != nil || status != "unknown" {
		t.Fatal("mismatched result accepted", status, err)
	}
	hub.result.Commit = snapshot.Commit
	api.tick(ctx)
	if err = pool.QueryRow(ctx, "SELECT status FROM stack_deployments WHERE id=$1", jobID).Scan(&status); err != nil || status != "succeeded" {
		t.Fatal("self-update result not recovered", status, err)
	}
}

func TestImportEndpointsRequireDeploymentPermission(t *testing.T) {
	for _, role := range []string{auth.RoleViewer, auth.RoleOperator} {
		mux := http.NewServeMux()
		Register(mux, Config{Authz: authorizer{auth.User{Role: role}}})
		for _, path := range []string{"/api/stacks/imports/discover", "/api/stacks/imports/preview", "/api/stacks/imports"} {
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(`{}`)))
			if w.Code != 403 {
				t.Fatal(role, path, w.Code)
			}
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/stacks/imports/recovery", nil))
		if w.Code != 403 {
			t.Fatal("recovery not protected", role, w.Code)
		}
	}
}
