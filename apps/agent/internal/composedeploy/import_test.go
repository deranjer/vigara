package composedeploy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	agentchannel "github.com/deranjer/vigara/apps/agent/internal/channel"
	"github.com/deranjer/vigara/internal/composesecrets"
	"github.com/deranjer/vigara/internal/protocol"
	"github.com/deranjer/vigara/internal/stack"
)

func existingFixture(t *testing.T) (*Runner, string, *[]string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("existing Compose deployments run on Linux")
	}
	root := t.TempDir()
	project := filepath.Join(root, "vigara")
	if err := os.MkdirAll(filepath.Join(project, "data", "postgres"), 0700); err != nil {
		t.Fatal(err)
	}
	compose := "services:\n  app:\n    image: example/vigara:latest\n    env_file: .env\n    environment:\n      URL: ${PUBLIC_URL}\n    volumes:\n      - ./data/postgres:/data\n"
	if err := os.WriteFile(filepath.Join(project, "compose.yml"), []byte(compose), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".env"), []byte("PUBLIC_URL=https://example.com\nPASSWORD=do-not-copy-plaintext\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Mounted application data must not be scanned as Compose configuration.
	if err := os.WriteFile(filepath.Join(project, "data", "postgres", "compose.yaml"), []byte("not a Compose project"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := []string{}
	r := &Runner{ManagedRoot: root, Targets: map[string]Target{}}
	r.runCommand = func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		switch args[0] {
		case "ps":
			return []byte(strings.Repeat("a", 64) + "\n"), nil
		case "inspect":
			return json.Marshal(map[string]any{"ID": strings.Repeat("a", 64), "Project": "existing-vigara", "Service": "app", "Directory": project, "Files": filepath.Join(project, "compose.yml"), "State": "running", "Started": "2026-11-01T00:00:00Z", "Mounts": []map[string]any{{"Source": filepath.Join(project, "data", "postgres"), "Destination": "/data", "Type": "bind", "RW": true}}})
		case "compose":
			if args[len(args)-1] != "--resolve-image-digests" {
				return nil, errCompose
			}
			if !strings.Contains(strings.Join(args, " "), "--project-directory "+project) {
				t.Error("original project directory lost")
			}
			for i, arg := range args {
				if arg == "-f" {
					data, err := os.ReadFile(args[i+1])
					if err != nil {
						t.Error(err)
					}
					if !strings.Contains(string(data), "env_file:") || strings.Contains(string(data), "env_file: .env") {
						t.Error("service environment was not remapped privately")
					}
				}
			}
			return []byte(`{"services":{"app":{"image":"example/vigara@sha256:` + strings.Repeat("b", 64) + `","volumes":[{"type":"bind","source":"` + filepath.Join(project, "data", "postgres") + `","target":"/data"}]}}}`), nil
		}
		return nil, errCompose
	}
	return r, project, &calls
}

func TestDeploymentRPCSurvivesServerDisconnectAndKeepsReadableJournal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Compose deployment runs on Linux")
	}
	r := testRunner(t)
	snapshot := testSnapshot()
	plan, err := r.Plan(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	base := r.runCommand
	started, release := make(chan struct{}), make(chan struct{})
	r.runCommand = func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		for _, arg := range args {
			if arg == "up" {
				close(started)
				<-release
				if ctx.Err() != nil {
					t.Error("server cancellation stopped the host deployment")
				}
				return nil, nil
			}
		}
		return base(ctx, dir, args...)
	}
	client := agentchannel.NewClient("https://example.com", "test-only", "test", false, nil, nil)
	Register(client, r)
	request := stack.DeployRequest{Job: stack.Job{ID: strings.Repeat("d", 64), Snapshot: snapshot}, Token: plan.Token, Images: plan.Images}
	data, _ := json.Marshal(request)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan stack.Result, 1)
	go func() {
		result, err := client.Calls[protocol.MethodComposeDeploy](ctx, data)
		if err != nil {
			t.Error(err)
			done <- stack.Result{}
			return
		}
		done <- result.(stack.Result)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("deployment did not reach Compose")
	}
	cancel()
	statusQuery, _ := json.Marshal(map[string]string{"stackId": snapshot.ID, "jobId": request.Job.ID})
	status, err := client.Calls[protocol.MethodComposeStatus](context.Background(), statusQuery)
	if err != nil || status.(stack.Result).Status != "running" {
		close(release)
		t.Fatal("journal unavailable while deployment is active", err)
	}
	close(release)
	select {
	case result := <-done:
		if result.Status != "succeeded" {
			t.Fatal("deployment failed after server disconnect", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deployment did not finish")
	}
}

func TestImportEncryptsAndRegistersWithoutChangingDeployment(t *testing.T) {
	r, project, calls := existingFixture(t)
	ctx := context.Background()
	discovery, err := r.DiscoverImports(ctx)
	if err != nil || len(discovery.Candidates) != 1 {
		t.Fatal(discovery, err)
	}
	c := discovery.Candidates[0]
	if c.ProjectName != "existing-vigara" || len(c.Issues) != 0 || len(c.Storage) != 1 {
		t.Fatal("unexpected discovery", c)
	}
	q := protocol.ComposeImportRequest{Directory: c.Directory, Token: c.Token, ID: "server", Name: "Vigara"}
	preview, err := r.PreviewImport(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(preview)
	if strings.Contains(string(encoded), "do-not-copy-plaintext") {
		t.Fatal("environment values leaked in review")
	}
	editorFile := filepath.Join(t.TempDir(), "editor.agekey")
	recoveryFile := filepath.Join(t.TempDir(), "recovery.agekey")
	q.EditorRecipient, err = composesecrets.EnsureAgeIdentity(editorFile)
	if err != nil {
		t.Fatal(err)
	}
	q.RecoveryRecipient, err = composesecrets.EnsureAgeIdentity(recoveryFile)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(project, ".env"))
	beforeCompose, _ := os.ReadFile(filepath.Join(project, "compose.yml"))
	prepared, err := r.PrepareImport(ctx, q)
	if err != nil {
		t.Fatal("prepare", err)
	}
	s := stack.Snapshot{ID: q.ID, Files: prepared.Files}
	m, err := stack.Validate(s)
	if err != nil || len(m.Compose.InterpolationFiles) != 1 || len(m.Compose.EnvironmentFiles) != 1 {
		t.Fatal("encrypted inputs missing", m, err)
	}
	for path, data := range s.Files {
		if strings.Contains(data, "do-not-copy-plaintext") || path == ".env" {
			t.Fatal("plaintext in prepared snapshot", path)
		}
	}
	for _, key := range []string{editorFile, recoveryFile, r.identityPath()} {
		plain, err := composesecrets.DecryptDotenv(ctx, "", key, []byte(s.Files[m.Compose.InterpolationFiles[0].Path]))
		if err != nil || string(plain) != string(before) {
			t.Fatal("recipient cannot recover imported environment", err)
		}
	}
	target, err := r.ConfirmImport(ctx, q)
	if err != nil || target.Directory != project || target.ProjectName != "existing-vigara" {
		t.Fatal("confirm", target, err)
	}
	if _, err = r.ConfirmImport(ctx, q); err != nil {
		t.Fatal("retry was not idempotent", err)
	}
	after, _ := os.ReadFile(filepath.Join(project, ".env"))
	afterCompose, _ := os.ReadFile(filepath.Join(project, "compose.yml"))
	if string(before) != string(after) || string(beforeCompose) != string(afterCompose) {
		t.Fatal("import modified original source files")
	}
	for _, call := range *calls {
		if strings.HasPrefix(call, "compose ") {
			t.Fatal("import invoked Compose", call)
		}
	}
	restarted := &Runner{ManagedRoot: r.ManagedRoot, Targets: map[string]Target{}, runCommand: r.runCommand}
	if persisted, ok := restarted.lookup("server"); !ok || persisted.Directory != project || !persisted.Imported {
		t.Fatal("import registry lost on restart")
	}
	s.Commit = strings.Repeat("c", 40)
	plan, err := restarted.Plan(ctx, s)
	if err != nil || len(plan.Images) != 1 {
		t.Fatal("imported environment preview failed", err)
	}
	q.ID = "duplicate"
	if _, err = r.ConfirmImport(ctx, q); err == nil {
		t.Fatal("second stack claimed the same runtime project")
	}
}

func TestImportRejectsChangedSourceAndSymlinkedEnvironment(t *testing.T) {
	r, project, _ := existingFixture(t)
	ctx := context.Background()
	discovery, err := r.DiscoverImports(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := discovery.Candidates[0]
	q := protocol.ComposeImportRequest{Directory: c.Directory, Token: c.Token, ID: "server", Name: "Vigara"}
	if err = os.WriteFile(filepath.Join(project, ".env"), []byte("PASSWORD=changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = r.PreviewImport(ctx, q); err == nil {
		t.Fatal("changed source accepted")
	}
	if _, err = r.ConfirmImport(ctx, q); err == nil {
		t.Fatal("changed source registered")
	}
	if err = os.Remove(filepath.Join(project, ".env")); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(t.TempDir(), "outside.env")
	if err = os.WriteFile(secret, []byte("PASSWORD=outside\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(secret, filepath.Join(project, ".env")); err != nil {
		t.Fatal(err)
	}
	if _, err = r.ConfirmImport(ctx, q); err == nil {
		t.Fatal("symlinked environment accepted")
	}
}

func TestImportObservationIgnoresOtherRootsButRetainsProjectConflicts(t *testing.T) {
	r, _, _ := existingFixture(t)
	base := r.runCommand
	for _, project := range []string{"unrelated", "existing-vigara"} {
		r.runCommand = func(ctx context.Context, dir string, args ...string) ([]byte, error) {
			data, err := base(ctx, dir, args...)
			if args[0] == "ps" {
				return append(data, []byte(strings.Repeat("b", 64)+"\n")...), err
			}
			if args[0] == "inspect" {
				other, _ := json.Marshal(map[string]any{"ID": strings.Repeat("b", 64), "Project": project, "Service": "other", "Directory": "/outside/approved/root", "Files": "/outside/approved/root/compose.yml", "State": "running", "Started": "2026-11-01T00:00:00Z"})
				return append(append(data, '\n'), other...), err
			}
			return data, err
		}
		discovery, err := r.DiscoverImports(context.Background())
		if project == "unrelated" {
			if err != nil || len(discovery.Candidates) != 1 || len(discovery.Candidates[0].Issues) != 0 {
				t.Fatal("unrelated project blocked import", discovery, err)
			}
		} else if err == nil && len(discovery.Candidates) == 1 && len(discovery.Candidates[0].Issues) == 0 {
			t.Fatal("partial project observation was accepted")
		}
	}
}

func TestImportInstallerTemplates(t *testing.T) {
	for _, name := range []string{"compose.staging.yml", "compose.caddy.example.yml", "compose.caddy-proxy.example.yml"} {
		t.Run(name, func(t *testing.T) {
			r, project, _ := existingFixture(t)
			data, err := os.ReadFile(filepath.Join("../../../../deploy", name))
			if err != nil {
				t.Fatal(err)
			}
			source := strings.ReplaceAll(string(data), "env_file: .env.staging", "env_file: .env")
			if err = os.WriteFile(filepath.Join(project, "compose.yml"), []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			discovery, err := r.DiscoverImports(context.Background())
			if err != nil || len(discovery.Candidates) != 1 {
				t.Fatal(discovery, err)
			}
			candidate := discovery.Candidates[0]
			if len(candidate.Issues) != 0 {
				t.Fatal("installer project cannot be imported", candidate.Issues)
			}
			q := protocol.ComposeImportRequest{ID: "server", Name: "Installer stack", Directory: candidate.Directory, Token: candidate.Token}
			q.EditorRecipient, err = composesecrets.EnsureAgeIdentity(filepath.Join(t.TempDir(), "editor.age"))
			if err != nil {
				t.Fatal(err)
			}
			q.RecoveryRecipient, err = composesecrets.EnsureAgeIdentity(filepath.Join(t.TempDir(), "recovery.age"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = r.PrepareImport(context.Background(), q); err != nil {
				t.Fatal("installer import preparation failed", err)
			}
		})
	}
}
