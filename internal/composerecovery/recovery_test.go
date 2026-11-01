package composerecovery

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T, cycle bool) string {
	t.Helper()
	root, err := os.MkdirTemp(".", ".compose-recovery-fixture-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cwd, _ := os.Getwd()
		if within(root, cwd) {
			_ = os.Chdir(filepath.Dir(root))
		}
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err == nil {
				if info.IsDir() {
					_ = os.Chmod(path, 0700)
				} else {
					_ = os.Chmod(path, 0600)
				}
			}
			return nil
		})
		for i := 0; i < 10; i++ {
			if err := os.RemoveAll(root); err == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Errorf("remove fixture: could not remove temporary Git repository")
	})
	run := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = root
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-q")
	run("config", "user.email", "fixture@example.invalid")
	run("config", "user.name", "fixture")
	dependency := "    - before: db\n      after: web\n      reason: database before frontend\n"
	if cycle {
		dependency += "    - before: web\n      after: db\n      reason: cycle\n"
	}
	write("profiles/prod/vigara-profile.yaml", "apiVersion: vigara.io/v1\nkind: ServerProfile\nprofileId: prod\ndisplayName: Production\nrequirements:\n  mounts:\n    - hostPath: /srv/app\n      required: true\n      purpose: persistent app data\n      persistent: true\n      backupPolicyId: app-data\n  networks: []\n  volumes: []\n  devices: []\n  startupDependencies:\n"+dependency)
	write("assignments/prod/db.yaml", `apiVersion: vigara.io/v1
kind: DeploymentAssignment
assignmentId: db-prod
stackId: db
profileId: prod
environment: production
ownership:
  mode: exclusive
  ownershipEpoch: 1
enabled: false
`)
	write("assignments/prod/web.yaml", `apiVersion: vigara.io/v1
kind: DeploymentAssignment
assignmentId: web-prod
stackId: web
profileId: prod
environment: production
ownership:
  mode: exclusive
  ownershipEpoch: 1
enabled: false
`)
	for _, id := range []string{"db", "web"} {
		write("stacks/"+id+"/vigara-stack.yaml", "apiVersion: vigara.io/v1\nkind: Stack\nstackId: "+id+"\ndisplayName: "+id+"\nprojectName: "+id+"\ncompose:\n  files: [compose.yaml]\n  projectDirectory: .\n  interpolationFiles: []\nconfigurationRevisionPolicy: sha256-git-tree-and-placement-inputs-v1\n")
	}
	write("stacks/db/compose.yaml", "services:\n  postgres:\n    image: postgres:16\n")
	write("stacks/web/compose.yaml", "services:\n  frontend:\n    image: example/frontend\n  worker:\n    image: example/worker\n")
	run("add", ".")
	run("commit", "-qm", "fixture")
	return root
}

func within(root, path string) bool {
	root, _ = filepath.Abs(root)
	path, _ = filepath.Abs(path)
	root = strings.TrimRight(root, string(os.PathSeparator)) + string(os.PathSeparator)
	return strings.HasPrefix(strings.ToLower(path+string(os.PathSeparator)), strings.ToLower(root))
}

func TestBuildProducesOrderedMetadataOnlyPlan(t *testing.T) {
	root := fixture(t, false)
	snapshot := &SnapshotMetadata{ID: "nightly-2026-01", ProfileID: "prod", BackupReferences: map[string]string{"app-data": "restic:nightly-17"}, RecoveryKeyRefs: []string{"recovery-main"}}
	plan, err := Build(context.Background(), root, "prod", snapshot, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Executable || plan.Status != "NOT_EXECUTABLE" || len(plan.Stacks) != 2 || plan.Stacks[0].StackID != "db" || plan.Stacks[1].StackID != "web" {
		t.Fatalf("unexpected plan order or status: %+v", plan)
	}
	if strings.Join(plan.Stacks[1].Services, ",") != "frontend,worker" {
		t.Fatalf("multi-service profile incomplete: %+v", plan.Stacks[1].Services)
	}
	if plan.Snapshot == nil || len(plan.Snapshot.BackupReferences) != 1 || plan.Snapshot.BackupReferences[0].Reference != "restic:nightly-17" {
		t.Fatalf("backup reference missing: %+v", plan.Snapshot)
	}
	if len(plan.Requirements.Mounts) != 1 || len(plan.Prerequisites) < 5 {
		t.Fatalf("requirements/prerequisites missing: %+v", plan)
	}
	encoded, _ := json.Marshal(plan)
	for _, secret := range []string{"postgres:16", "example/frontend"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("plan includes unnecessary content %q", secret)
		}
	}
	second, err := Build(context.Background(), root, "prod", snapshot, nil)
	if err != nil {
		t.Fatal(err)
	}
	encodedAgain, _ := json.Marshal(second)
	if string(encoded) != string(encodedAgain) {
		t.Fatal("plan output is not deterministic")
	}
}

func TestBuildIncludesSelectedReleaseMetadataOnly(t *testing.T) {
	root := fixture(t, false)
	releaseID := strings.Repeat("a", 64)
	releasePath := filepath.Join(root, "releases", "prod", "web", releaseID+".yaml")
	if err := os.MkdirAll(filepath.Dir(releasePath), 0700); err != nil {
		t.Fatal(err)
	}
	body := "apiVersion: vigara.io/v1\nkind: Release\nreleaseId: " + releaseID + "\nprofileId: prod\nstackId: web\ngitCommit: " + strings.Repeat("b", 40) + "\noutcome: succeeded\n"
	if err := os.WriteFile(releasePath, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	run := exec.Command("git", "add", "releases")
	run.Dir = root
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v %s", err, out)
	}
	run = exec.Command("git", "commit", "-qm", "release fixture")
	run.Dir = root
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v %s", err, out)
	}
	snapshot := &SnapshotMetadata{ID: "nightly-2026-01", ProfileID: "prod", BackupReferences: map[string]string{"app-data": "restic:nightly-17"}}
	plan, err := Build(context.Background(), root, "prod", snapshot, []string{releaseID})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Releases) != 1 || plan.Releases[0].StackID != "web" || plan.Releases[0].Outcome != "succeeded" {
		t.Fatalf("release summary missing: %+v", plan.Releases)
	}
}

func TestBuildDoesNotReadEncryptedSOPSBlob(t *testing.T) {
	root := fixture(t, false)
	marker := "CONTENT_SECRET_MARKER"
	path := filepath.Join(root, "stacks", "web", "secrets", "runtime.env.sops")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat(marker, 16<<10)), 0600); err != nil {
		t.Fatal(err)
	}
	add := exec.Command("git", "add", "stacks/web/secrets/runtime.env.sops")
	add.Dir = root
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v %s", err, out)
	}
	commit := exec.Command("git", "commit", "-qm", "encrypted input fixture")
	commit.Dir = root
	if out, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v %s", err, out)
	}
	snapshot := &SnapshotMetadata{ID: "nightly-2026-01", ProfileID: "prod", BackupReferences: map[string]string{"app-data": "restic:nightly-17"}}
	plan, err := Build(context.Background(), root, "prod", snapshot, nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(plan)
	if strings.Contains(string(encoded), marker) {
		t.Fatal("encrypted content appeared in plan output")
	}
}

func TestBuildFailsClosedWithoutPersistentBackupReference(t *testing.T) {
	root := fixture(t, false)
	if _, err := Build(context.Background(), root, "prod", nil, nil); err == nil {
		t.Fatal("persistent resource accepted without selected snapshot")
	}
	snapshot := &SnapshotMetadata{ID: "nightly-2026-01", ProfileID: "prod", BackupReferences: map[string]string{}}
	if _, err := Build(context.Background(), root, "prod", snapshot, nil); err == nil {
		t.Fatal("missing backup reference accepted")
	}
}

func TestBuildRejectsDependencyCycle(t *testing.T) {
	if _, err := Build(context.Background(), fixture(t, true), "prod", nil, nil); err == nil {
		t.Fatal("cycle accepted")
	}
}

func TestBuildRejectsDirtyTrackedInputs(t *testing.T) {
	root := fixture(t, false)
	p := filepath.Join(root, "stacks", "web", "compose.yaml")
	if err := os.WriteFile(p, []byte("services: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(context.Background(), root, "prod", &SnapshotMetadata{ID: "x", ProfileID: "prod", BackupReferences: map[string]string{"app-data": "ref"}}, nil); err == nil {
		t.Fatal("dirty tracked input accepted")
	}
}

func TestBuildValidatesOtherProfilesWithoutRequiringTheirBackups(t *testing.T) {
	root := fixture(t, false)
	addText(t, root, "profiles/secondary/vigara-profile.yaml", `apiVersion: vigara.io/v1
kind: ServerProfile
profileId: secondary
displayName: Secondary
requirements:
  mounts:
    - hostPath: /srv/secondary
      required: true
      purpose: secondary data
      persistent: true
      backupPolicyId: secondary-data
`)
	addText(t, root, "assignments/secondary/secondary.yaml", `apiVersion: vigara.io/v1
kind: DeploymentAssignment
assignmentId: secondary-prod
stackId: secondary
profileId: secondary
environment: production
ownership:
  mode: exclusive
  ownershipEpoch: 1
enabled: false
`)
	addText(t, root, "stacks/secondary/vigara-stack.yaml", "apiVersion: vigara.io/v1\nkind: Stack\nstackId: secondary\ndisplayName: Secondary\nprojectName: secondary\ncompose:\n  files: [compose.yaml]\n  projectDirectory: .\n  interpolationFiles: []\nconfigurationRevisionPolicy: sha256-git-tree-and-placement-inputs-v1\n")
	addText(t, root, "stacks/secondary/compose.yaml", "services:\n  app:\n    image: example/secondary\n")
	commitFixture(t, root, "secondary profile")
	snapshot := &SnapshotMetadata{ID: strings.Repeat("c", 64), ProfileID: "prod", BackupReferences: map[string]string{"app-data": "restic:nightly-17"}}
	plan, err := Build(context.Background(), root, "prod", snapshot, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Stacks) != 2 || len(plan.Snapshot.BackupReferences) != 1 || plan.Snapshot.ID != strings.Repeat("c", 64) {
		t.Fatalf("other profile affected selected recovery plan: %+v", plan)
	}
}

func TestBuildSupportsStrictOfflineStackAssetValidation(t *testing.T) {
	root := fixture(t, false)
	path := filepath.Join(root, "stacks", "web", "vigara-stack.yaml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := string(body) + "configurationAssets:\n  - path: /srv/webdata\n    class: persistent-data\n    required: true\n    backupPolicyId: web-data\n"
	if err := os.WriteFile(path, []byte(updated), 0600); err != nil {
		t.Fatal(err)
	}
	commitFixture(t, root, "persistent stack asset")
	snapshot := &SnapshotMetadata{ID: "nightly-2026-01", ProfileID: "prod", BackupReferences: map[string]string{"app-data": "restic:app", "web-data": "restic:web"}}
	plan, err := Build(context.Background(), root, "prod", snapshot, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(plan.Stacks[1].PersistentPaths, ","), "/srv/webdata") {
		t.Fatalf("persistent stack asset omitted: %+v", plan.Stacks[1])
	}
	delete(snapshot.BackupReferences, "web-data")
	if _, err := Build(context.Background(), root, "prod", snapshot, nil); err == nil {
		t.Fatal("persistent asset without backup reference accepted")
	}
}

func addText(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}
func commitFixture(t *testing.T, root, message string) {
	t.Helper()
	add := exec.Command("git", "add", ".")
	add.Dir = root
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v %s", err, out)
	}
	commit := exec.Command("git", "commit", "-qm", message)
	commit.Dir = root
	if out, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v %s", err, out)
	}
}
