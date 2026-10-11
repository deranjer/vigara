package composeadoption

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDiscoverPreservesRuntimeProjectAndSeparatesDiskFromRuntime(t *testing.T) {
	root := t.TempDir()
	appDir := filepath.Join(root, "lore")
	mustMkdir(t, appDir)
	compose := "services:\n  web:\n    image: example/web\n    env_file: .env\n    environment:\n      TOKEN: ${SHARED_TOKEN}\n    ports:\n      - 8080:80\n    volumes:\n      - ./data:/data\n"
	mustWrite(t, filepath.Join(appDir, "compose.yaml"), compose)
	mustWrite(t, filepath.Join(appDir, ".env"), "PASSWORD=do-not-print\n")
	mustWrite(t, filepath.Join(root, ".env"), "SHARED_TOKEN=also-secret\n")
	snapshot := RuntimeSnapshot{Complete: true, ObservedAt: time.Now().UTC(), Containers: []RuntimeContainer{
		{ID: "abcdef0123456789", ProjectName: "lore-server", ServiceName: "web", WorkingDir: appDir, ComposeFiles: []string{"compose.yaml"}, State: "running", StartedAt: "2026-10-11T10:00:00Z", StorageBinds: []StorageBind{{Source: "/mnt/lore", Destination: "/data", ReadWrite: true}}},
		{ID: "0123456789abcdef", ProjectName: "orphan-project", ServiceName: "legacy", WorkingDir: filepath.Join(root, "removed"), ComposeFiles: []string{"compose.yaml"}, State: "exited", StartedAt: "2026-10-10T10:00:00Z"},
	}}
	preview, err := Discover(root, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Applications) != 1 {
		t.Fatalf("applications = %d", len(preview.Applications))
	}
	app := preview.Applications[0]
	if app.ProjectName != "lore-server" || app.RuntimeStatus != "running" {
		t.Fatalf("identity/status = %q/%q", app.ProjectName, app.RuntimeStatus)
	}
	if len(app.DotenvFiles) != 2 || app.DotenvFiles[0].Path != "lore/.env" || app.DotenvFiles[0].Sensitivity != "encrypted" {
		t.Fatalf("dotenv inputs = %#v", app.DotenvFiles)
	}
	if len(app.SharedEnvFiles) != 1 || app.SharedEnvFiles[0].Path != ".env" {
		t.Fatalf("shared candidates = %#v", app.SharedEnvFiles)
	}
	if len(app.SharedEnvVariables) != 1 || app.SharedEnvVariables[0] != "SHARED_TOKEN" {
		t.Fatalf("shared variables = %#v", app.SharedEnvVariables)
	}
	if !app.Requirements.HostNetwork && len(app.Requirements.Ports) != 1 {
		t.Fatalf("requirements missing ports: %#v", app.Requirements)
	}
	if len(preview.OrphanedProjects) != 1 || preview.OrphanedProjects[0].ProjectName != "orphan-project" {
		t.Fatalf("orphan projects = %#v", preview.OrphanedProjects)
	}
	encoded := strings.Join([]string{app.Directory, app.ProjectName, app.DotenvFiles[0].Path, app.SharedEnvFiles[0].Path}, " ")
	if strings.Contains(encoded, "do-not-print") || strings.Contains(encoded, "also-secret") {
		t.Fatal("dotenv value leaked into preview")
	}
}

func TestDiscoverDoesNotInferStoppedFromIncompleteRuntimeInventory(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "compose.yml"), "services:\n  web:\n    image: example/web\n")
	preview, err := Discover(root, RuntimeSnapshot{Complete: false})
	if err != nil {
		t.Fatal(err)
	}
	if got := preview.Applications[0].RuntimeStatus; got != "runtime_unknown" {
		t.Fatalf("status = %q", got)
	}
}

func TestDiscoverRequiresCompleteMatchingRuntimeAndUniformState(t *testing.T) {
	root := t.TempDir()
	appDir := filepath.Join(root, "app")
	mustMkdir(t, appDir)
	mustWrite(t, filepath.Join(appDir, "compose.yaml"), "services:\n  web:\n    image: example/web\n")
	container := RuntimeContainer{ID: "abcdef0123456789", ProjectName: "runtime-app", ServiceName: "web", WorkingDir: appDir, ComposeFiles: []string{"compose.yaml"}, State: "running", StartedAt: "2026-10-11T10:00:00Z"}
	partial := RuntimeSnapshot{Complete: false, ObservedAt: time.Now().UTC(), Containers: []RuntimeContainer{container}}
	preview, err := Discover(root, partial)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Applications[0].ProjectName != "" || preview.Applications[0].RuntimeStatus != "runtime_unknown" {
		t.Fatalf("partial inventory was matched: %#v", preview.Applications[0])
	}
	second := container
	second.ID = "0123456789abcdef"
	second.ServiceName = "worker"
	second.State = "restarting"
	complete := RuntimeSnapshot{Complete: true, ObservedAt: time.Now().UTC(), Containers: []RuntimeContainer{container, second}}
	preview, err = Discover(root, complete)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Applications[0].RuntimeStatus != "runtime_unknown" {
		t.Fatalf("mixed status = %q", preview.Applications[0].RuntimeStatus)
	}
}

func TestDiscoverRejectsStaleOrFutureCompleteObservations(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "compose.yaml"), "services:\n  web:\n    image: example/web\n")
	for _, captured := range []time.Time{time.Now().Add(-6 * time.Minute), time.Now().Add(2 * time.Minute)} {
		if _, err := Discover(root, RuntimeSnapshot{Complete: true, ObservedAt: captured, Containers: []RuntimeContainer{}}); err == nil {
			t.Fatal("stale or future runtime observation was accepted")
		}
	}
}

func TestSensitivePrivateKeyAssignmentIsReportedWithoutValue(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "compose.yaml"), "services:\n  web:\n    image: example/web\n    command: 'echo PRIVATE_KEY=must-not-print'\n")
	preview, err := Discover(root, RuntimeSnapshot{Complete: false})
	if err != nil {
		t.Fatal(err)
	}
	app := preview.Applications[0]
	if !has(app.ReviewRequired, "compose_metadata_requires_plaintext_secret_review") {
		t.Fatalf("review = %#v", app.ReviewRequired)
	}
	if strings.Contains(strings.Join(app.ReviewRequired, " ")+strings.Join(app.Unsupported, " "), "must-not-print") {
		t.Fatal("secret literal leaked into diagnostics")
	}
}

func TestDiscoverRejectsEscapingAndMissingInputsWithoutPrintingValues(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "app")
	mustMkdir(t, app)
	mustWrite(t, filepath.Join(app, "docker-compose.yaml"), "services:\n  web:\n    image: example/web\n    env_file: ../../outside.env\n    environment:\n      PASSWORD: never-report-this\n")
	preview, err := Discover(root, RuntimeSnapshot{Complete: false})
	if err != nil {
		t.Fatal(err)
	}
	got := preview.Applications[0]
	if !has(got.Unsupported, "nonportable_configuration_path") {
		t.Fatalf("unsupported = %#v", got.Unsupported)
	}
	if !has(got.ReviewRequired, "service_environment_may_contain_plaintext_secrets") {
		t.Fatalf("review = %#v", got.ReviewRequired)
	}
	if strings.Contains(strings.Join(got.Unsupported, " ")+strings.Join(got.ReviewRequired, " "), "never-report-this") {
		t.Fatal("Compose value leaked into diagnostic")
	}
}

func TestVerifyUnchangedChecksIDsStartTimesProjectAndMountBindings(t *testing.T) {
	workdir, err := filepath.Abs(filepath.Join("srv", "app"))
	if err != nil {
		t.Fatal(err)
	}
	before := RuntimeSnapshot{Complete: true, ObservedAt: time.Now().UTC(), Containers: []RuntimeContainer{{ID: "container-a", ProjectName: "project", ServiceName: "web", WorkingDir: workdir, ComposeFiles: []string{"compose.yaml"}, StartedAt: "2026-10-11T09:00:00Z", State: "running", StorageBinds: []StorageBind{{Source: "/data/a", Destination: "/data", ReadWrite: true}}}}}
	after := before
	after.Containers = append([]RuntimeContainer(nil), before.Containers...)
	after.ObservedAt = before.ObservedAt.Add(time.Minute)
	if err := VerifyUnchanged(before, after); err != nil {
		t.Fatalf("same snapshot: %v", err)
	}
	after.Containers[0].StartedAt = "2026-10-11T09:01:00Z"
	if err := VerifyUnchanged(before, after); err == nil {
		t.Fatal("start time change was accepted")
	}
	after.Containers[0] = before.Containers[0]
	after.Containers[0].StorageBinds = append([]StorageBind(nil), before.Containers[0].StorageBinds...)
	after.Containers[0].StorageBinds[0].Source = "/data/b"
	if err := VerifyUnchanged(before, after); err == nil {
		t.Fatal("storage binding change was accepted")
	}
	if err := VerifyUnchanged(RuntimeSnapshot{}, after); err == nil {
		t.Fatal("incomplete observation was accepted")
	}
}

func TestProjectStatusDistinguishesStopped(t *testing.T) {
	if got := projectStatus([]RuntimeContainer{{ID: "x", State: "exited"}}); got != "stopped" {
		t.Fatalf("status = %q", got)
	}
}

func TestDiscoverInventoryLargerThanLegacyTelemetryCap(t *testing.T) {
	root := t.TempDir()
	snapshot := RuntimeSnapshot{Complete: true, ObservedAt: time.Now().UTC(), Containers: []RuntimeContainer{}}
	for i := 0; i < 42; i++ {
		dir := filepath.Join(root, fmt.Sprintf("app-%02d", i))
		mustMkdir(t, dir)
		mustWrite(t, filepath.Join(dir, "compose.yaml"), "services:\n  web:\n    image: example/web\n")
		services := 1
		if i < 18 {
			services = 2
		}
		for j := 0; j < services; j++ {
			started := time.Now().UTC().Format(time.RFC3339Nano)
			snapshot.Containers = append(snapshot.Containers, RuntimeContainer{ID: fmt.Sprintf("%064x", i*2+j+1), ProjectName: fmt.Sprintf("project-%02d", i), ServiceName: fmt.Sprintf("service-%d", j), WorkingDir: dir, ComposeFiles: []string{"compose.yaml"}, State: "running", StartedAt: started})
		}
	}
	if len(snapshot.Containers) != 60 {
		t.Fatalf("fixture containers = %d", len(snapshot.Containers))
	}
	preview, err := Discover(root, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Applications) != 42 {
		t.Fatalf("applications = %d", len(preview.Applications))
	}
	for _, app := range preview.Applications {
		if app.RuntimeStatus != "running" {
			t.Fatalf("%s status = %q", app.Directory, app.RuntimeStatus)
		}
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
}
func mustWrite(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}
func has(v []string, want string) bool {
	for _, s := range v {
		if s == want {
			return true
		}
	}
	return false
}
