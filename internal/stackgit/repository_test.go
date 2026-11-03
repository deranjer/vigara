package stackgit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deranjer/vigara/internal/stack"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@localhost", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@localhost", "GIT_CONFIG_NOSYSTEM=1")
	b, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("git %v: %v %s", args, e, b)
	}
	return strings.TrimSpace(string(b))
}
func fixture(t *testing.T) (*Repository, string) {
	t.Helper()
	if _, e := exec.LookPath("git"); e != nil {
		t.Skip("git required")
	}
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	git(t, root, "init", "--bare", remote)
	git(t, root, "init", "-b", "main", seed)
	if e := os.WriteFile(filepath.Join(seed, "README.md"), []byte("Stack repository\n"), 0600); e != nil {
		t.Fatal(e)
	}
	git(t, seed, "add", "README.md")
	git(t, seed, "commit", "-m", "Initialize")
	git(t, seed, "push", remote, "main")
	return &Repository{Directory: filepath.Join(root, "cache.git"), Remote: remote, Branch: "main"}, seed
}
func sample(id string) stack.Snapshot {
	return stack.Snapshot{ID: id, Files: map[string]string{
		"vigara-stack.yaml": "apiVersion: vigara.io/v1\nkind: Stack\nstackId: " + id + "\ndisplayName: Test\nprojectName: " + id + "\ncompose:\n  files: [compose.yaml]\n  projectDirectory: .\n  interpolationFiles: []\nconfigurationRevisionPolicy: sha256-git-tree-and-placement-inputs-v1\n",
		"compose.yaml":      "# Keep this comment\nservices:\n  web:\n    image: nginx:stable\n",
	}}
}
func TestSaveSyncConflictAndRestore(t *testing.T) {
	r, _ := fixture(t)
	ctx := context.Background()
	s := sample("web")
	first, e := r.Save(ctx, s, "alice")
	if e != nil || first.State != "saved" {
		t.Fatalf("save: %+v %v", first, e)
	}
	loaded, e := r.Read(ctx, "web", "")
	if e != nil || !stack.Equal(loaded.Files, s.Files) {
		t.Fatalf("read: %+v %v", loaded, e)
	}
	history, e := r.History(ctx, "web")
	if e != nil || len(history) != 1 || history[0].Commit != first.Commit {
		t.Fatalf("history: %+v %v", history, e)
	}
	// Another stack can advance the branch without conflicting with this stack's base.
	if _, e = r.Save(ctx, sample("other"), "bob"); e != nil {
		t.Fatal(e)
	}
	loaded.Files["compose.yaml"] = "services:\n  web:\n    image: nginx:1.27\n"
	second, e := r.Save(ctx, loaded, "alice")
	if e != nil || second.State != "saved" {
		t.Fatalf("unrelated change: %+v %v", second, e)
	}
	stale := sample("web")
	stale.Commit = first.Commit
	stale.Files["compose.yaml"] = "services:\n  web:\n    image: nginx:1.26\n"
	if _, e = r.Save(ctx, stale, "bob"); !errors.Is(e, ErrConflict) {
		t.Fatalf("stale draft overwritten: %v", e)
	}
	restored := sample("web")
	restored.Commit = second.Commit
	third, e := r.Save(ctx, restored, "alice")
	if e != nil || third.Commit == first.Commit {
		t.Fatalf("restore must be new commit: %+v %v", third, e)
	}
	old, e := r.Read(ctx, "web", first.Commit)
	if e != nil || !strings.HasPrefix(old.Files["compose.yaml"], "# Keep") {
		t.Fatal("old revision or comment lost", e)
	}
	if _, e = r.Read(ctx, "other", first.Commit); !errors.Is(e, ErrNotFound) {
		t.Fatalf("unexpected historical stack: %v", e)
	}
	// Retrying a save whose response was lost does not produce another commit or conflict.
	retry, e := r.Save(ctx, restored, "alice")
	if e != nil || retry.Commit != third.Commit {
		t.Fatalf("retry: %+v %v", retry, e)
	}
}
func TestRejectUnsupportedGitEntries(t *testing.T) {
	r, seed := fixture(t)
	ctx := context.Background()
	s := sample("web")
	saved, e := r.Save(ctx, s, "alice")
	if e != nil {
		t.Fatal(e)
	}
	git(t, seed, "pull", r.Remote, "main")
	git(t, seed, "update-index", "--chmod=+x", "stacks/web/compose.yaml")
	git(t, seed, "commit", "-m", "Executable input")
	git(t, seed, "push", r.Remote, "main")
	if _, e = r.Read(ctx, "web", ""); !errors.Is(e, stack.ErrInvalid) {
		t.Fatalf("executable accepted: %v", e)
	}
	items, e := r.List(ctx)
	if e != nil || len(items) != 1 || len(items[0].Files) != 0 {
		t.Fatalf("invalid stack should remain listed for review: %+v %v", items, e)
	}
	if _, e = r.Read(ctx, "web", saved.Commit); e != nil {
		t.Fatal(e)
	}
}
func TestRejectedPushIsNotDeployable(t *testing.T) {
	r, _ := fixture(t)
	ctx := context.Background()
	hook := filepath.Join(r.Remote, "hooks", "pre-receive")
	if e := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0700); e != nil {
		t.Fatal(e)
	}
	saved, e := r.Save(ctx, sample("web"), "alice")
	if e != nil || saved.State != "pendingPush" {
		t.Fatalf("pending: %+v %v", saved, e)
	}
	if _, e = r.Read(ctx, "web", saved.Commit); !errors.Is(e, ErrNotFound) {
		t.Fatalf("unconfirmed revision deployable: %v", e)
	}
	if e = os.Remove(hook); e != nil {
		t.Fatal(e)
	}
	saved, e = r.Save(ctx, sample("web"), "alice")
	if e != nil || saved.State != "saved" {
		t.Fatalf("retry push: %+v %v", saved, e)
	}
}

func TestReadBatchedBlobsPreservesBytesAndLimits(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		valid bool
	}{
		{"contents", map[string]string{"empty.txt": "", "no-newline.txt": "hello", "multiline.txt": "one\r\ntwo\n", "binary.txt": "a\x00b\n", "unicode.txt": "caf\u00e9 \u4e16\u754c", "limit.txt": strings.Repeat("x", 128<<10)}, true},
		{"oversized-file", map[string]string{"large.txt": strings.Repeat("x", (128<<10)+1)}, false},
		{"oversized-stack", map[string]string{"a.txt": strings.Repeat("x", 128<<10), "b.txt": strings.Repeat("x", 128<<10), "c.txt": strings.Repeat("x", 128<<10), "d.txt": "x"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, seed := fixture(t)
			dir := filepath.Join(seed, "stacks", "demo")
			if e := os.MkdirAll(dir, 0700); e != nil {
				t.Fatal(e)
			}
			for path, content := range tc.files {
				if e := os.WriteFile(filepath.Join(dir, path), []byte(content), 0600); e != nil {
					t.Fatal(e)
				}
			}
			git(t, seed, "-c", "core.autocrlf=false", "add", ".")
			git(t, seed, "commit", "-m", "Add files")
			git(t, seed, "push", r.Remote, "main")
			s, e := r.Read(context.Background(), "demo", "")
			if tc.valid {
				if e != nil || !stack.Equal(s.Files, tc.files) {
					t.Fatalf("contents changed: %v", e)
				}
			} else if !errors.Is(e, stack.ErrInvalid) {
				t.Fatalf("oversized input accepted: %v", e)
			}
		})
	}
}

func TestReadBatchedBlobsFileCount(t *testing.T) {
	r, seed := fixture(t)
	dir := filepath.Join(seed, "stacks", "demo")
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 65; i++ {
		if e := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file-%02d.txt", i)), []byte("x"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	git(t, seed, "add", ".")
	git(t, seed, "commit", "-m", "Too many files")
	git(t, seed, "push", r.Remote, "main")
	if _, e := r.Read(context.Background(), "demo", ""); !errors.Is(e, stack.ErrInvalid) {
		t.Fatalf("file count limit ignored: %v", e)
	}
}
