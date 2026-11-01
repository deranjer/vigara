package composedeploy

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/deranjer/vigara/internal/stack"
)

// TestComposeReleaseDrill is explicitly opt-in because it starts a disposable
// workload on the local Docker engine. Run only on an authorized test host.
func TestComposeReleaseDrill(t *testing.T) {
	if os.Getenv("VIGARA_COMPOSE_RELEASE_DRILL") != "1" {
		t.Skip("set VIGARA_COMPOSE_RELEASE_DRILL=1 on an authorized disposable test host")
	}
	if runtime.GOOS != "linux" {
		t.Skip("Linux Compose runner")
	}
	image := os.Getenv("VIGARA_COMPOSE_RELEASE_DRILL_IMAGE")
	if image == "" {
		image = "nginx:alpine"
	}
	seed := make([]byte, 8)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	project := "vigara-release-drill-" + hex.EncodeToString(seed)
	dir := t.TempDir()
	r := &Runner{Targets: map[string]Target{"demo": {ProjectName: project, Directory: dir}}}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	// Refuse even an improbable project collision before any Docker mutation.
	for _, kind := range []string{"container", "network"} {
		args := []string{kind, "ls", "--quiet", "--filter", "label=com.docker.compose.project=" + project}
		if kind == "container" {
			args = append(args, "--all")
		}
		out, err := r.command(ctx, dir, args...)
		if err != nil || len(strings.TrimSpace(string(out))) != 0 {
			t.Fatal("could not establish an empty unique drill project", err)
		}
	}
	s := testSnapshot()
	s.Files["vigara-stack.yaml"] = strings.ReplaceAll(s.Files["vigara-stack.yaml"], "original-project", project)
	encoded, _ := json.Marshal(map[string]any{"services": map[string]any{"web": map[string]any{"image": image, "healthcheck": map[string]any{"test": []string{"CMD", "nginx", "-t"}, "interval": "1s", "timeout": "5s", "retries": 10}}}})
	s.Files["compose.yaml"] = string(encoded)
	plan, err := r.Plan(ctx, s)
	if err != nil {
		t.Fatal("real Compose digest preview failed", err)
	}
	if !validImages(plan.Images) {
		t.Fatal("preview did not return immutable images")
	}
	base := []string{"compose", "-p", project, "--project-directory", dir, "-f", filepath.Join(dir, "compose.yaml")}
	// Cleanup is restricted to this random project and requires checking every
	// existing container/network's exact Compose project label first.
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		for _, kind := range []string{"container", "network"} {
			args := []string{kind, "ls", "--quiet", "--filter", "label=com.docker.compose.project=" + project}
			if kind == "container" {
				args = append(args, "--all")
			}
			out, err := r.command(cleanupCtx, dir, args...)
			if err != nil {
				t.Error("cleanup could not list unique project resources")
				return
			}
			for _, id := range strings.Fields(string(out)) {
				if !stack.CommitPattern.MatchString(id) && len(id) != 12 {
					t.Error("cleanup refused unexpected resource identity")
					return
				}
				format := "{{index .Labels \"com.docker.compose.project\"}}"
				if kind == "container" {
					format = "{{index .Config.Labels \"com.docker.compose.project\"}}"
				}
				label, err := r.command(cleanupCtx, dir, kind, "inspect", "--format", format, id)
				if err != nil || strings.TrimSpace(string(label)) != project {
					t.Error("cleanup refused resource with unexpected project label")
					return
				}
			}
		}
		if _, err := r.command(cleanupCtx, dir, append(base, "down", "--volumes", "--remove-orphans")...); err != nil {
			t.Error("unique disposable drill cleanup failed")
		}
	})
	jobHash := sha256.Sum256(seed)
	q := Request{Job: stack.Job{ID: hex.EncodeToString(jobHash[:]), Snapshot: s, Pull: true}, Token: plan.Token, Images: plan.Images}
	result, err := r.Deploy(ctx, q)
	if err != nil || result.Status != "succeeded" || result.Readiness != "compose-wait-passed" || !stack.Equal(result.Images, plan.Images) {
		t.Fatalf("real deployment did not confirm pinned release: %+v %v", result, err)
	}
	ids, err := r.command(ctx, dir, append(base, "ps", "--all", "--quiet", "web")...)
	if err != nil || len(strings.Fields(string(ids))) != 1 {
		t.Fatal("expected exactly one drill container", err)
	}
	id := strings.TrimSpace(string(ids))
	data, err := r.command(ctx, dir, "container", "inspect", "--format", "{{json .}}", id)
	var container struct {
		Config struct {
			Image  string
			Labels map[string]string
		}
		State struct {
			Running bool
			Health  struct{ Status string }
		}
	}
	if err != nil || json.Unmarshal(data, &container) != nil || container.Config.Image != plan.Images["web"] || container.Config.Labels["com.docker.compose.project"] != project || !container.State.Running || container.State.Health.Status != "healthy" {
		t.Fatal("running container image, project or health did not match immutable release", err)
	}
	repeated, err := r.Deploy(ctx, q)
	if err != nil || repeated.Status != "succeeded" || repeated.PlanToken != result.PlanToken {
		t.Fatal("journal retry failed", repeated, err)
	}
	after, err := r.command(ctx, dir, append(base, "ps", "--all", "--quiet", "web")...)
	if err != nil || string(after) != string(ids) {
		t.Fatal("journal retry replaced the drill container", err)
	}
	t.Logf("verified immutable image %s, Compose health and unchanged container on journal retry for unique project %s", plan.Images["web"], project)
}
