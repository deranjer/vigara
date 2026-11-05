package composedeploy

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/deranjer/vigara/internal/composesecrets"
	"github.com/deranjer/vigara/internal/stack"
	"gopkg.in/yaml.v3"
)

func TestDeploymentLeavesStandaloneRecoveryAfterWorkspaceCleanup(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux deployment and POSIX recovery script")
	}
	r := testRunner(t)
	// Exercise shell quoting and invocation from a different working directory.
	target := r.Targets["demo"]
	target.Directory = filepath.Join(target.Directory, "stack with 'quotes'")
	if err := os.Mkdir(target.Directory, 0700); err != nil {
		t.Fatal(err)
	}
	r.Targets["demo"] = target
	r.DockerHost = "unix:///var/run/docker.sock"
	r.Identity = filepath.Join(t.TempDir(), "identity.age")
	recipient, err := composesecrets.EnsureAgeIdentity(r.Identity)
	if err != nil {
		t.Fatal(err)
	}
	recipients := []string{recipient}
	for _, name := range []string{"editor.age", "recovery.age"} {
		public, err := composesecrets.EnsureAgeIdentity(filepath.Join(t.TempDir(), name))
		if err != nil {
			t.Fatal(err)
		}
		recipients = append(recipients, public)
	}
	s := testSnapshot()
	m, err := stack.Validate(s)
	if err != nil {
		t.Fatal(err)
	}
	plains := []string{"POSTGRES_PASSWORD='first $value'\n", "POSTGRES_PASSWORD='second $value'\nMULTILINE=\"line one\nline two\"\n"}
	for i, name := range []string{"shared", "assignment"} {
		path := "secrets/" + name + ".age"
		cipher, err := composesecrets.EncryptAgeDotenv(context.Background(), []byte(plains[i]), recipients)
		if err != nil {
			t.Fatal(err)
		}
		s.Files[path] = string(cipher)
		m.Compose.InterpolationFiles = append(m.Compose.InterpolationFiles, stack.Input{Path: path, Purpose: "deployment", Sensitivity: "encrypted", Format: "age-dotenv"})
	}
	m.Compose.EnvironmentFiles = []stack.Input{{Path: "secrets/shared.age", Target: "config/service.env", Purpose: "deployment", Sensitivity: "encrypted", Format: "age-dotenv"}}
	m.Compose.Files = append(m.Compose.Files, "override.yaml")
	s.Files["override.yaml"] = "services:\n  web:\n    restart: unless-stopped\n"
	m.SecretRecipientRefs = []stack.RecipientRef{{Class: "central-editor", KeyRef: "editor"}, {Class: "target-agent", KeyRef: "target"}, {Class: "operator-recovery", KeyRef: "recovery"}}
	manifest, err := yaml.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	s.Files["vigara-stack.yaml"] = string(manifest)
	plan, err := r.Plan(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	var deployed []string
	r.runCommand = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		for i, arg := range args {
			if arg == "up" {
				deployed = append([]string{}, args[:i]...)
			}
		}
		return []byte(`{"services":{"web":{"image":"` + testImage + `"}}}`), nil
	}
	q := Request{Job: stack.Job{ID: strings.Repeat("b", 64), Snapshot: s}, Token: plan.Token, Images: plan.Images}
	result, err := r.Deploy(context.Background(), q)
	if err != nil || result.Status != "succeeded" || len(deployed) == 0 {
		t.Fatalf("deployment failed: %+v %v", result, err)
	}
	inputs := []string{}
	for i, arg := range deployed {
		if arg == "--env-file" {
			inputs = append(inputs, deployed[i+1])
		}
	}
	if len(inputs) != 2 {
		t.Fatal("lost interpolation order", deployed)
	}
	for i, file := range inputs {
		data, err := os.ReadFile(file)
		info, statErr := os.Stat(file)
		if err != nil || statErr != nil || string(data) != plains[i] || info.Mode().Perm() != 0600 || !strings.HasPrefix(file, filepath.Join(target.Directory, recoveryDirectory)+"/") {
			t.Fatal("recovery input missing, changed or not private", err, statErr)
		}
	}
	serviceEnv, err := os.ReadFile(filepath.Join(target.Directory, "config/service.env"))
	if err != nil || string(serviceEnv) != plains[0] {
		t.Fatal("service environment not retained", err)
	}
	for _, file := range []string{recoveryDirectory, filepath.Dir(inputs[0]), "start.sh"} {
		if !filepath.IsAbs(file) {
			file = filepath.Join(target.Directory, file)
		}
		info, err := os.Stat(file)
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatal("recovery directory or launcher not private", file, err)
		}
	}
	// A fake Docker executable records the invocation after Deploy has returned
	// and its temporary workspace has been removed. No daemon, keys or Git needed.
	bin := t.TempDir()
	capture := filepath.Join(bin, "arguments")
	fake := "#!/bin/sh\n[ -z \"${POSTGRES_PASSWORD+x}${COMPOSE_FILE+x}${COMPOSE_PROFILES+x}\" ] || exit 12\n[ \"$DOCKER_HOST\" = 'unix:///var/run/docker.sock' ] || exit 13\n[ \"$COMPOSE_DISABLE_ENV_FILE\" = 1 ] || exit 14\nprintf '%s\\n' \"$@\" > " + shellQuote(capture) + "\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(fake), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(target.Directory, "start.sh"))
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin", "POSTGRES_PASSWORD=wrong", "COMPOSE_FILE=wrong", "COMPOSE_PROFILES=wrong")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("standalone script failed: %v %s", err, out)
	}
	data, err := os.ReadFile(capture)
	expected := append(deployed, "up", "--detach", "--no-build", "--pull", "never")
	if err != nil || !reflect.DeepEqual(strings.Split(strings.TrimSuffix(string(data), "\n"), "\n"), expected) {
		t.Fatal("recovery differs from deployed project, files, environment or images", err)
	}
	script, _ := os.ReadFile(filepath.Join(target.Directory, "start.sh"))
	if strings.Contains(string(script), "second $value") || strings.Contains(string(script), r.Identity) {
		t.Fatal("launcher embedded a secret or requires the age identity")
	}
}

func TestRecoveryRefusesUnsafePathsAndPreservesExistingLauncherOnFailure(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux recovery files")
	}
	for _, scenario := range []string{"directory symlink", "launcher symlink", "user launcher", "missing input", "invalid compose"} {
		t.Run(scenario, func(t *testing.T) {
			r := testRunner(t)
			target := r.Targets["demo"]
			m, _ := stack.Validate(testSnapshot())
			previous := recoveryHeader + "# previous\n"
			launcher := filepath.Join(target.Directory, "start.sh")
			if err := os.WriteFile(launcher, []byte(previous), 0700); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "directory symlink":
				if err := os.Symlink(t.TempDir(), filepath.Join(target.Directory, recoveryDirectory)); err != nil {
					t.Fatal(err)
				}
			case "launcher symlink":
				os.Remove(launcher)
				if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), launcher); err != nil {
					t.Fatal(err)
				}
			case "user launcher":
				previous = "#!/bin/sh\necho custom\n"
				os.WriteFile(launcher, []byte(previous), 0700)
			case "missing input":
				m.Compose.InterpolationFiles = []stack.Input{{Path: "secrets/missing.age"}}
			case "invalid compose":
				r.runCommand = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("secret diagnostic") }
			}
			_, err := r.recovery(context.Background(), target, m, t.TempDir(), "/unused/images.json")
			if err == nil {
				t.Fatal("unsafe or incomplete recovery accepted")
			}
			if scenario != "launcher symlink" {
				data, _ := os.ReadFile(launcher)
				if string(data) != previous {
					t.Fatal("failed preparation replaced previous launcher")
				}
			}
			if scenario != "directory symlink" {
				entries, _ := os.ReadDir(filepath.Join(target.Directory, recoveryDirectory))
				if len(entries) != 0 {
					t.Fatal("failed preparation retained plaintext bundle")
				}
			}
		})
	}
}

func TestRecoveryReservesGeneratedPaths(t *testing.T) {
	for _, path := range []string{"start.sh", "start.sh/child", ".vigara-recovery/input.env"} {
		s := testSnapshot()
		m, _ := stack.Validate(s)
		s.Files[path] = "user input"
		if validateRecoveryPaths(s, m) == nil {
			t.Fatal("repository can overwrite generated recovery", path)
		}
		delete(s.Files, path)
		m.Compose.EnvironmentFiles = []stack.Input{{Target: path}}
		if validateRecoveryPaths(s, m) == nil {
			t.Fatal("environment target can overwrite generated recovery", path)
		}
	}
}

func TestRecoveryRefreshRemovesPreviousPlaintextInputs(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux recovery files")
	}
	r := testRunner(t)
	target := r.Targets["demo"]
	m, _ := stack.Validate(testSnapshot())
	m.Compose.InterpolationFiles = []stack.Input{{Path: "secrets/old.age"}}
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, ".vigara-input-0"), []byte("OLD_PASSWORD=old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	args, err := r.recovery(context.Background(), target, m, workspace, "/unused/images.json")
	if err != nil {
		t.Fatal(err)
	}
	var previousInput string
	for i, arg := range args {
		if arg == "--env-file" {
			previousInput = args[i+1]
		}
	}
	ignored, err := os.ReadFile(filepath.Join(filepath.Dir(previousInput), ".gitignore"))
	if err != nil || string(ignored) != "*\n" {
		t.Fatal("plaintext bundle is not ignored by Git", err)
	}
	m.Compose.InterpolationFiles = nil
	if _, err := r.recovery(context.Background(), target, m, workspace, "/unused/images.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(previousInput); !os.IsNotExist(err) {
		t.Fatal("obsolete plaintext input was retained", err)
	}
	entries, err := os.ReadDir(filepath.Join(target.Directory, recoveryDirectory))
	if err != nil || len(entries) != 1 {
		t.Fatal("unexpected recovery bundle count", len(entries), err)
	}
}
