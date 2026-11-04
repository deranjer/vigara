package composedeploy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/deranjer/vigara/internal/stack"
)

const testImage = "nginx@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func testSnapshot() stack.Snapshot {
	return stack.Snapshot{ID: "demo", Commit: strings.Repeat("a", 40), Files: map[string]string{
		"vigara-stack.yaml": "apiVersion: vigara.io/v1\nkind: Stack\nstackId: demo\ndisplayName: Demo\nprojectName: original-project\ncompose:\n  files: [compose.yaml]\n  projectDirectory: .\n  interpolationFiles: []\nconfigurationRevisionPolicy: sha256-git-tree-and-placement-inputs-v1\n",
		"compose.yaml":      "services:\n  web:\n    image: nginx\n",
	}}
}
func testRunner(t *testing.T) *Runner {
	t.Helper()
	return &Runner{Targets: map[string]Target{"demo": {ProjectName: "original-project", Directory: t.TempDir()}}, runCommand: func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`{"services":{"web":{"image":"` + testImage + `"}}}`), nil
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
		return []byte(`{"services":{"web":{"image":"` + testImage + `","volumes":[{"type":"bind","source":"` + source + `","target":"/data"}]}}}`), nil
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
	q := Request{Job: stack.Job{ID: strings.Repeat("b", 64), Snapshot: s}, Token: plan.Token, Images: plan.Images}
	upCalls := 0
	r.runCommand = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "--resolve-image-digests") {
			t.Fatal("deployment resolved mutable tags again")
		}
		if strings.Contains(joined, " up ") {
			upCalls++
			if !strings.Contains(joined, "--pull never") || !strings.Contains(joined, "--wait") {
				t.Fatal("unsafe apply", args)
			}
			var override string
			for i, a := range args {
				if a == "-f" {
					override = args[i+1]
				}
			}
			data, err := os.ReadFile(override)
			if err != nil || !strings.Contains(string(data), testImage) {
				t.Fatal("apply omitted pinned image", string(data), err)
			}
		}
		return []byte(`{"services":{"web":{"image":"` + testImage + `"}}}`), nil
	}
	result, e := r.Deploy(context.Background(), q)
	if e != nil || result.Status != "succeeded" {
		t.Fatalf("deployment: %+v %v", result, e)
	}
	if upCalls != 1 || result.Images["web"] != testImage || result.ConfigurationRevision != stack.ConfigurationRevision(s) || result.Commit != s.Commit || result.Readiness != "compose-wait-passed" {
		t.Fatalf("incomplete release result %+v; apply calls %d", result, upCalls)
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

func TestInterruptedOrMismatchedJournalNeverRunsAgain(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("deployment filesystem operations are Linux-first")
	}
	for _, status := range []string{"running", "succeeded", "corrupt"} {
		t.Run(status, func(t *testing.T) {
			r := testRunner(t)
			s := testSnapshot()
			p, err := r.Plan(context.Background(), s)
			if err != nil {
				t.Fatal(err)
			}
			q := Request{Job: stack.Job{ID: strings.Repeat("f", 64), Snapshot: s}, Token: p.Token, Images: p.Images}
			journal := filepath.Join(r.Targets["demo"].Directory, ".vigara-jobs")
			if err = os.Mkdir(journal, 0700); err != nil {
				t.Fatal(err)
			}
			result := stack.Result{Status: status, JobID: q.Job.ID, Commit: s.Commit, ConfigurationRevision: stack.ConfigurationRevision(s), PlanToken: p.Token, Images: p.Images}
			if status == "succeeded" {
				result.Commit = strings.Repeat("c", 40)
			}
			data, _ := json.Marshal(result)
			if status == "corrupt" {
				data = []byte("{")
			}
			if err = os.WriteFile(filepath.Join(journal, q.Job.ID+".json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			r.runCommand = func(context.Context, string, ...string) ([]byte, error) {
				t.Fatal("uncertain/reused job executed commands")
				return nil, nil
			}
			got, err := r.Deploy(context.Background(), q)
			if status == "running" {
				if err != nil || got.Status != "unknown" {
					t.Fatal(got, err)
				}
			} else if !errors.Is(err, errJournal) {
				t.Fatal("mismatched or corrupt journal accepted", got, err)
			}
		})
	}
}

func TestDeploymentRechecksPlacementRequirementsBeforeWriting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("deployment filesystem operations are Linux-first")
	}
	r := testRunner(t)
	s := testSnapshot()
	mount := filepath.Join(t.TempDir(), "mounted-data")
	if err := os.Mkdir(mount, 0700); err != nil {
		t.Fatal(err)
	}
	profile, _ := json.Marshal(map[string]any{"apiVersion": "vigara.io/v1", "kind": "ServerProfile", "profileId": "profile", "displayName": "Test", "requirements": map[string]any{"mounts": []any{map[string]any{"hostPath": mount, "required": true, "purpose": "application data"}}}})
	s.Placement = &stack.Placement{ProfileID: "profile", AssignmentID: "demo-profile", Epoch: 1, ProfileManifest: string(profile), AssignmentManifest: `{"apiVersion":"vigara.io/v1","kind":"DeploymentAssignment","assignmentId":"demo-profile","stackId":"demo","profileId":"profile","environment":"test","enabled":true,"ownership":{"mode":"exclusive","ownershipEpoch":1}}`}
	p, err := r.Plan(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(mount); err != nil {
		t.Fatal(err)
	}
	r.runCommand = func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("unavailable placement ran Compose")
		return nil, nil
	}
	got, err := r.Deploy(context.Background(), Request{Job: stack.Job{ID: strings.Repeat("e", 64), Snapshot: s}, Token: p.Token, Images: p.Images})
	if err != nil || got.Status != "failed" || got.Stage != "validate" || got.ConfigurationRevision != stack.ConfigurationRevision(s) {
		t.Fatal(got, err)
	}
	if _, err = os.Stat(filepath.Join(r.Targets["demo"].Directory, "compose.yaml")); !os.IsNotExist(err) {
		t.Fatal("preflight failure wrote configuration", err)
	}
}

func TestFailedApplyPersistsReleaseWithoutClaimingReadiness(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("deployment filesystem operations are Linux-first")
	}
	r := testRunner(t)
	s := testSnapshot()
	p, err := r.Plan(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	q := Request{Job: stack.Job{ID: strings.Repeat("d", 64), Snapshot: s}, Token: p.Token, Images: p.Images}
	r.runCommand = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), " up ") {
			return nil, errors.New("secret command diagnostics must not leak")
		}
		return []byte(`{"services":{"web":{"image":"` + testImage + `"}}}`), nil
	}
	got, err := r.Deploy(context.Background(), q)
	if err != nil || got.Status != "failed" || got.Stage != "apply" || got.Readiness != "compose-wait-failed" || got.JobID != q.Job.ID || got.ConfigurationRevision != stack.ConfigurationRevision(s) || !stack.Equal(got.Images, p.Images) || strings.Contains(got.Message, "secret") {
		t.Fatal("failed release was not recorded honestly", got, err)
	}
	r.runCommand = func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("failed job was silently retried")
		return nil, nil
	}
	repeated, err := r.Deploy(context.Background(), q)
	if err != nil || repeated.Status != "failed" || repeated.Readiness != "compose-wait-failed" {
		t.Fatal("failed journal not recovered", repeated, err)
	}
}

func TestImageOverrideDoesNotFollowExistingSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires host privileges")
	}
	outside := filepath.Join(t.TempDir(), "sentinel")
	if err := os.WriteFile(outside, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	override := filepath.Join(t.TempDir(), "images.json")
	if err := os.Symlink(outside, override); err != nil {
		t.Fatal(err)
	}
	if err := imageOverride(override, map[string]string{"web": testImage}); !errors.Is(err, errJournal) {
		t.Fatal("existing override symlink was accepted", err)
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "unchanged" {
		t.Fatal("image override followed symlink", string(data), err)
	}
	info, err := os.Lstat(override)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("refused override should leave the symlink untouched", err)
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
	result, e := r.Deploy(context.Background(), Request{Job: stack.Job{ID: strings.Repeat("c", 64), Snapshot: s}, Token: plan.Token, Images: plan.Images})
	if e != nil || result.Status != "failed" {
		t.Fatalf("stale preview: %+v %v", result, e)
	}
	content, _ := os.ReadFile(dest)
	if string(content) != "external edit" {
		t.Fatal("stale preview overwrote external edit")
	}
}

func TestModelResolvesImagesAndBindsDigestInToken(t *testing.T) {
	r := testRunner(t)
	s := testSnapshot()
	m, _ := stack.Validate(s)
	r.runCommand = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if !strings.Contains(strings.Join(args, " "), "config --format json") {
			t.Fatal(args)
		}
		if !strings.Contains(strings.Join(args, " "), "--resolve-image-digests") {
			return []byte(`{"services":{"web":{"image":"nginx:stable"}}}`), nil
		}
		return []byte(`{"services":{"web":{"image":"` + testImage + `"}}}`), nil
	}
	p, err := r.model(context.Background(), s, r.Targets["demo"], m)
	if err != nil || p.Images["web"] != testImage || p.Tracking["web"] != "nginx:stable" {
		t.Fatalf("plan %+v: %v", p, err)
	}
	changed := map[string]string{"web": strings.Replace(testImage, "aaaa", "bbbb", 1)}
	token, err := releaseToken(r.Targets["demo"], s, changed)
	if err != nil || token == p.Token {
		t.Fatal("image change did not invalidate token", err)
	}
}

func TestModelRejectsUnresolvedOrUnsupportedImages(t *testing.T) {
	for _, image := range []string{"nginx:latest", "nginx@sha256:abc", "nginx@sha256:" + strings.Repeat("a", 64) + "\n"} {
		r := testRunner(t)
		s := testSnapshot()
		m, _ := stack.Validate(s)
		r.runCommand = func(context.Context, string, ...string) ([]byte, error) {
			b, _ := json.Marshal(map[string]any{"services": map[string]any{"web": map[string]string{"image": image}}})
			return b, nil
		}
		if _, err := r.model(context.Background(), s, r.Targets["demo"], m); err == nil {
			t.Fatal("accepted unresolved image", image)
		}
	}
}

func TestPinnedModelUsesExactImageOverrideWithoutTagResolution(t *testing.T) {
	r := testRunner(t)
	s := testSnapshot()
	m, _ := stack.Validate(s)
	images := map[string]string{"web": testImage}
	r.runCommand = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "--resolve-image-digests") {
			t.Fatal("deploy must not resolve tags again")
		}
		var override string
		for i, a := range args {
			if a == "-f" && i+1 < len(args) {
				override = args[i+1]
			}
		}
		b, err := os.ReadFile(override)
		if err != nil || !strings.Contains(string(b), testImage) {
			t.Fatal("missing immutable override", string(b), err)
		}
		return []byte(`{"services":{"web":{"image":"` + testImage + `"}}}`), nil
	}
	if _, err := r.modelWithImages(context.Background(), s, r.Targets["demo"], m, images); err != nil {
		t.Fatal(err)
	}
	if _, err := r.modelWithImages(context.Background(), s, r.Targets["demo"], m, map[string]string{"other": testImage}); err == nil {
		t.Fatal("different service set accepted")
	}
}
