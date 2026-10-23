package composedeploy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/deranjer/vigara/internal/stack"
)

func testSnapshot() stack.Snapshot {
	return stack.Snapshot{ID: "demo", Commit: strings.Repeat("a", 40), Files: map[string]string{
		"vigara-stack.yaml": "apiVersion: vigara.io/v1\nkind: Stack\nstackId: demo\ndisplayName: Demo\nprojectName: original-project\ncompose:\n  files: [compose.yaml]\n  projectDirectory: .\n  interpolationFiles: []\nconfigurationRevisionPolicy: sha256-git-tree-and-placement-inputs-v1\n",
		"compose.yaml":      "services:\n  web:\n    image: nginx\n",
	}}
}
func testRunner(t *testing.T) *Runner {
	t.Helper()
	return &Runner{Targets: map[string]Target{"demo": {ProjectName: "original-project", Directory: t.TempDir()}}, runCommand: func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`{"services":{"web":{"image":"nginx"}}}`), nil
	}}
}

func TestPlanRejectsExternalFileReadsBeforeCompose(t *testing.T) {
	for _, source := range []string{
		"include: /etc/compose.yaml\nservices: {}\n",
		"services:\n  web:\n    image: nginx\n    env_file: /etc/secrets.env\n",
		"services:\n  web:\n    extends:\n      file: /etc/compose.yaml\n      service: web\n",
		"services:\n  web:\n    image: nginx\n    post_start:\n      - command: echo secret\n",
	} {
		t.Run(source, func(t *testing.T) {
			r := testRunner(t)
			r.runCommand = func(context.Context, string, ...string) ([]byte, error) {
				t.Fatal("Compose must not run on unsupported file inputs")
				return nil, nil
			}
			s := testSnapshot()
			s.Files["compose.yaml"] = source
			m, e := stack.Validate(s)
			if e != nil {
				return
			}
			if _, e = r.model(context.Background(), s, r.Targets["demo"], m); e == nil {
				t.Fatal("unsafe input accepted")
			}
		})
	}
}
func TestFingerprintDetectsLocalEditsAndMissingFiles(t *testing.T) {
	r := testRunner(t)
	s := testSnapshot()
	target := r.Targets["demo"]
	before, e := fingerprint(target, s)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(target.Directory, "compose.yaml"), []byte("local changes"), 0600); e != nil {
		t.Fatal(e)
	}
	after, e := fingerprint(target, s)
	if e != nil || before == after {
		t.Fatal("local edit did not invalidate preview", e)
	}
	if e = os.Mkdir(filepath.Join(target.Directory, "vigara-stack.yaml"), 0700); e != nil {
		t.Fatal(e)
	}
	if _, e = fingerprint(target, s); e == nil {
		t.Fatal("directory accepted as configuration file")
	}
}
func TestModelPreservesRelativeStorageAndBlocksMissingBinds(t *testing.T) {
	r := testRunner(t)
	s := testSnapshot()
	m, e := stack.Validate(s)
	if e != nil {
		t.Fatal(e)
	}
	target := r.Targets["demo"]
	// The resolved validation-workspace path must be checked against the original project's storage.
	r.runCommand = func(_ context.Context, dir string, args ...string) ([]byte, error) {
		source := filepath.ToSlash(filepath.Join(dir, "data"))
		return []byte(`{"services":{"web":{"image":"nginx","volumes":[{"type":"bind","source":"` + source + `","target":"/data"}]}}}`), nil
	}
	if _, e = r.model(context.Background(), s, target, m); e == nil {
		t.Fatal("missing bind source accepted")
	}
	if e = os.Mkdir(filepath.Join(target.Directory, "data"), 0700); e != nil {
		t.Fatal(e)
	}
	if _, e = r.model(context.Background(), s, target, m); e != nil {
		t.Fatal(e)
	}
	r.runCommand = func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("raw secret diagnostic")
	}
	if _, e = r.model(context.Background(), s, target, m); e == nil {
		t.Fatal("missing Compose validation accepted")
	}
}
func TestDeploymentIsPinnedAndIdempotent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("deployment filesystem operations are Linux-first")
	}
	r := testRunner(t)
	s := testSnapshot()
	plan, e := r.Plan(context.Background(), s)
	if e != nil {
		t.Fatal(e)
	}
	q := Request{Job: stack.Job{ID: strings.Repeat("b", 64), Snapshot: s}, Token: plan.Token}
	result, e := r.Deploy(context.Background(), q)
	if e != nil || result.Status != "succeeded" {
		t.Fatalf("deployment: %+v %v", result, e)
	}
	r.runCommand = func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("completed job must never execute twice")
		return nil, nil
	}
	result, e = r.Deploy(context.Background(), q)
	if e != nil || result.Status != "succeeded" {
		t.Fatalf("repeat: %+v %v", result, e)
	}
}
func TestStalePreviewDoesNotWriteFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("deployment filesystem operations are Linux-first")
	}
	r := testRunner(t)
	s := testSnapshot()
	plan, e := r.Plan(context.Background(), s)
	if e != nil {
		t.Fatal(e)
	}
	dest := filepath.Join(r.Targets["demo"].Directory, "compose.yaml")
	if e = os.WriteFile(dest, []byte("external edit"), 0600); e != nil {
		t.Fatal(e)
	}
	result, e := r.Deploy(context.Background(), Request{Job: stack.Job{ID: strings.Repeat("c", 64), Snapshot: s}, Token: plan.Token})
	if e != nil || result.Status != "failed" {
		t.Fatalf("stale preview: %+v %v", result, e)
	}
	content, _ := os.ReadFile(dest)
	if string(content) != "external edit" {
		t.Fatal("stale preview overwrote external edit")
	}
}
