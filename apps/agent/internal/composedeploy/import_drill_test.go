package composedeploy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/deranjer/vigara/internal/composeadoption"
	"github.com/deranjer/vigara/internal/composesecrets"
	"github.com/deranjer/vigara/internal/protocol"
	"github.com/deranjer/vigara/internal/stack"
)

// Opt-in drill owns a uniquely named temporary Compose project and volume.
// Import must leave its actual container identity/start time/storage unchanged.
func TestImportRunningProjectDockerDrill(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getenv("VIGIRA_TEST_IMPORT_DOCKER") != "1" {
		t.Skip("set VIGIRA_TEST_IMPORT_DOCKER=1 on a Linux Docker test host")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	root := t.TempDir()
	directory := filepath.Join(root, "existing")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	project := fmt.Sprintf("vigara-import-drill-%d", time.Now().UnixNano())
	compose := "services:\n  app:\n    image: busybox:1.37\n    command: [sh, -c, 'sleep 600']\n    env_file: .env\n    volumes: [state:/state]\nvolumes:\n  state: {}\n"
	if err := os.WriteFile(filepath.Join(directory, "compose.yml"), []byte(compose), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, ".env"), []byte("PASSWORD=real-import-drill-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	docker := func(args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(ctx, "docker", args...)
		data, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("test Docker operation failed: %v %s", err, data)
		}
		return data
	}
	args := []string{"compose", "-p", project, "--project-directory", directory, "-f", filepath.Join(directory, "compose.yml")}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		command := exec.CommandContext(cleanup, "docker", append(args, "down", "--volumes", "--remove-orphans")...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Errorf("clean up test project: %v %s", err, output)
		}
	})
	docker(append(append([]string{}, args...), "up", "-d", "--pull", "never")...)
	r := &Runner{ManagedRoot: root, Targets: map[string]Target{}, DockerHost: os.Getenv("DOCKER_HOST")}
	observe := func() composeadoption.RuntimeSnapshot {
		t.Helper()
		observed, err := r.observeImports(ctx)
		if err != nil {
			t.Fatal(err)
		}
		selected := observed.Containers[:0]
		for _, container := range observed.Containers {
			if container.ProjectName == project {
				selected = append(selected, container)
			}
		}
		observed.Containers = selected
		return observed
	}
	before := observe()
	discovery, err := r.DiscoverImports(ctx)
	if err != nil || len(discovery.Candidates) != 1 {
		t.Fatal("discovery", discovery, err)
	}
	keyDir := t.TempDir()
	editor, err := composesecrets.EnsureAgeIdentity(filepath.Join(keyDir, "editor"))
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := composesecrets.EnsureAgeIdentity(filepath.Join(keyDir, "recovery"))
	if err != nil {
		t.Fatal(err)
	}
	q := protocol.ComposeImportRequest{Directory: "existing", Token: discovery.Candidates[0].Token, ID: "imported", Name: "Imported", EditorRecipient: editor, RecoveryRecipient: recovery}
	prepared, err := r.PrepareImport(ctx, q)
	if err != nil {
		t.Fatal("prepare", err)
	}
	if _, err = r.ConfirmImport(ctx, q); err != nil {
		t.Fatal("confirm", err)
	}
	after := observe()
	if err = composeadoption.VerifyUnchanged(before, after); err != nil {
		t.Fatal("import changed running containers", err)
	}
	snapshot := stack.Snapshot{ID: q.ID, Commit: strings.Repeat("a", 40), Files: prepared.Files}
	manifest, err := stack.Validate(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	target, ok := r.lookup(q.ID)
	if !ok {
		t.Fatal("registered target missing")
	}
	digest := strings.TrimSpace(string(docker("image", "inspect", "busybox:1.37", "--format", "{{index .RepoDigests 0}}")))
	plan, err := r.modelWithImages(ctx, snapshot, target, manifest, map[string]string{"app": digest})
	if err != nil {
		t.Fatal("private environment validation", err)
	}
	// An explicit later deployment must reuse the observed named volume and
	// the encrypted environment. This is separate from the non-mutating import.
	result, err := r.Deploy(ctx, stack.DeployRequest{Job: stack.Job{ID: strings.Repeat("b", 64), Snapshot: snapshot}, Token: plan.Token, Images: plan.Images})
	if err != nil || result.Status != "succeeded" {
		t.Fatal("deploy imported stack", result, err)
	}
	final := observe()
	if len(final.Containers) != 1 || len(final.Containers[0].StorageBinds) != 1 || final.Containers[0].StorageBinds[0].Name != before.Containers[0].StorageBinds[0].Name {
		t.Fatal("deployment moved existing storage")
	}
	var env []string
	if err = json.Unmarshal(docker("inspect", "--format", "{{json .Config.Env}}", final.Containers[0].ID), &env); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, value := range env {
		if value == "PASSWORD=real-import-drill-secret" {
			found = true
		}
	}
	if !found {
		t.Fatal("deployment lost encrypted service environment")
	}
}
