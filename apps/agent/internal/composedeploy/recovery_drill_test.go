package composedeploy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/deranjer/vigara/internal/composesecrets"
	"github.com/deranjer/vigara/internal/stack"
	"gopkg.in/yaml.v3"
)

// Opt-in: creates and removes only a unique disposable Compose project.
func TestStandaloneRecoveryDockerDrill(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getenv("VIGIRA_TEST_RECOVERY_DOCKER") != "1" {
		t.Skip("set VIGIRA_TEST_RECOVERY_DOCKER=1 on a Linux Docker test host")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	seed := make([]byte, 16)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	project := "vigara-recovery-drill-" + hex.EncodeToString(seed)
	dir := t.TempDir()
	r := &Runner{Targets: map[string]Target{"demo": {ProjectName: project, Directory: dir}}}
	// Use an already-cached immutable image; this drill never needs a registry.
	image, err := r.command(ctx, dir, "image", "inspect", "alpine:3.22", "--format", "{{index .RepoDigests 0}}")
	if err != nil || !immutableImage.MatchString(strings.TrimSpace(string(image))) {
		t.Fatal("drill needs a cached alpine:3.22 image with its repository digest")
	}
	for _, kind := range []string{"container", "network", "volume"} {
		args := []string{kind, "ls", "--quiet", "--filter", "label=com.docker.compose.project=" + project}
		if kind == "container" {
			args = append(args, "--all")
		}
		out, err := r.command(ctx, dir, args...)
		if err != nil || len(strings.TrimSpace(string(out))) != 0 {
			t.Fatal("could not establish an empty disposable project", err)
		}
	}
	s := testSnapshot()
	m, _ := stack.Validate(s)
	m.ProjectName = project
	r.Identity = filepath.Join(t.TempDir(), "target.age")
	recipients := []string{}
	for _, file := range []string{r.Identity, filepath.Join(t.TempDir(), "editor.age"), filepath.Join(t.TempDir(), "recovery.age")} {
		public, err := composesecrets.EnsureAgeIdentity(file)
		if err != nil {
			t.Fatal(err)
		}
		recipients = append(recipients, public)
	}
	cipher, err := composesecrets.EncryptAgeDotenv(ctx, []byte("POSTGRES_PASSWORD='test recovery $value'\nSERVICE_VALUE=runtime\n"), recipients)
	if err != nil {
		t.Fatal(err)
	}
	s.Files["secrets/runtime.age"] = string(cipher)
	m.Compose.InterpolationFiles = []stack.Input{{Path: "secrets/runtime.age", Purpose: "stack", Sensitivity: "encrypted", Format: "age-dotenv"}}
	m.Compose.EnvironmentFiles = []stack.Input{{Path: "secrets/runtime.age", Target: "service.env", Purpose: "deployment", Sensitivity: "encrypted", Format: "age-dotenv"}}
	m.SecretRecipientRefs = []stack.RecipientRef{{Class: "central-editor", KeyRef: "editor"}, {Class: "target-agent", KeyRef: "target"}, {Class: "operator-recovery", KeyRef: "recovery"}}
	manifest, _ := yaml.Marshal(m)
	s.Files["vigara-stack.yaml"] = string(manifest)
	model, _ := json.Marshal(map[string]any{
		"services": map[string]any{"web": map[string]any{
			"image": strings.TrimSpace(string(image)), "command": []string{"sh", "-c", "sleep infinity"},
			"environment": map[string]string{"PASSWORD": "${POSTGRES_PASSWORD:?required}"},
			"env_file":    "service.env", "volumes": []string{"data:/data"},
		}}, "volumes": map[string]any{"data": map[string]any{}},
	})
	s.Files["compose.yaml"] = string(model)
	plan, err := r.Plan(ctx, s)
	if err != nil {
		t.Fatal("preview", err)
	}
	base := []string{"compose", "-p", project, "--project-directory", dir, "-f", filepath.Join(dir, "compose.yaml")}
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		// Ignore ambient inputs and provide the synthetic interpolation value
		// explicitly for cleanup, even after the agent identity was removed.
		cleanupEnv := filepath.Join(dir, "cleanup.env")
		if err := os.WriteFile(cleanupEnv, []byte("POSTGRES_PASSWORD=cleanup\n"), 0600); err != nil {
			t.Error(err)
			return
		}
		args := append(append([]string{}, base...), "--env-file", cleanupEnv, "down", "--volumes")
		if _, err := r.command(cleanupCtx, dir, args...); err != nil {
			t.Error("disposable project cleanup failed", err)
		}
	})
	q := Request{Job: stack.Job{ID: strings.Repeat("c", 64), Snapshot: s}, Token: plan.Token, Images: plan.Images}
	result, err := r.Deploy(ctx, q)
	if err != nil || result.Status != "succeeded" {
		t.Fatalf("deploy: %+v %v", result, err)
	}
	ids, err := r.command(ctx, dir, "ps", "--all", "--quiet", "--filter", "label=com.docker.compose.project="+project)
	if err != nil || len(strings.Fields(string(ids))) != 1 {
		t.Fatal("expected one disposable container", err)
	}
	oldID := strings.TrimSpace(string(ids))
	if _, err := r.command(ctx, dir, "exec", oldID, "sh", "-c", "printf original-data > /data/marker"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.command(ctx, dir, "container", "rm", "--force", oldID); err != nil {
		t.Fatal(err)
	}
	// No agent identity, Vigara service or source repository is available now.
	if err := os.Remove(r.Identity); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, filepath.Join(dir, "start.sh"))
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "POSTGRES_PASSWORD=wrong", "COMPOSE_FILE=/missing", "COMPOSE_PROFILES=wrong")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("standalone start: %v %s", err, out)
	}
	ids, err = r.command(ctx, dir, "ps", "--quiet", "--filter", "label=com.docker.compose.project="+project)
	if err != nil || len(strings.Fields(string(ids))) != 1 || strings.TrimSpace(string(ids)) == oldID {
		t.Fatal("missing container was not recreated", err)
	}
	newID := strings.TrimSpace(string(ids))
	check := `[ "$PASSWORD" = 'test recovery $value' ] && [ "$SERVICE_VALUE" = runtime ] && [ "$(cat /data/marker)" = original-data ]`
	if _, err := r.command(ctx, dir, "exec", newID, "sh", "-c", check); err != nil {
		t.Fatal("recovered container lost interpolation, service environment or volume data", err)
	}
	actualImage, err := r.command(ctx, dir, "inspect", "--format", "{{.Config.Image}}", newID)
	if err != nil || strings.TrimSpace(string(actualImage)) != plan.Images["web"] {
		t.Fatal("recovery changed the reviewed image digest", err)
	}
	t.Log("recreated missing container without identity, preserved image, environment and volume data")
}
