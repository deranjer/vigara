package composedeploy

import (
	"context"
	"github.com/deranjer/vigara/internal/protocol"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestManagedTargetsPersistAndFenceProjects(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux Compose runner")
	}
	root := filepath.Join(t.TempDir(), "managed")
	command := func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	r := &Runner{ManagedRoot: root, Targets: map[string]Target{}, runCommand: command}
	q := protocol.ComposeCreateTarget{ID: "media", ProjectName: "home-media"}
	target, e := r.CreateTarget(context.Background(), q)
	if e != nil {
		t.Fatal(e)
	}
	if target.Directory != filepath.Join(root, "media") {
		t.Fatal("wrong directory")
	}
	restarted := &Runner{ManagedRoot: root, runCommand: command}
	if existing, ok := restarted.lookup("media"); !ok || existing.ProjectName != "home-media" {
		t.Fatal("target did not survive restart")
	}
	if _, e = restarted.CreateTarget(context.Background(), q); e != nil {
		t.Fatal("retry was not idempotent", e)
	}
	if _, e = restarted.CreateTarget(context.Background(), protocol.ComposeCreateTarget{ID: "other", ProjectName: "home-media"}); e == nil {
		t.Fatal("duplicate project accepted")
	}
	if _, e = restarted.CreateTarget(context.Background(), protocol.ComposeCreateTarget{ID: "media", ProjectName: "changed"}); e == nil {
		t.Fatal("ownership changed")
	}
}
func TestManagedTargetsRejectUnsafeAndExistingFolders(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux Compose runner")
	}
	root := t.TempDir()
	r := &Runner{ManagedRoot: root, runCommand: func(context.Context, string, ...string) ([]byte, error) { return nil, nil }}
	for _, id := range []string{"../escape", "../managed", "UPPER", ".hidden", "a/b"} {
		if _, e := r.CreateTarget(context.Background(), protocol.ComposeCreateTarget{ID: id, ProjectName: "test"}); e == nil {
			t.Fatal("unsafe ID accepted", id)
		}
	}
	if e := os.Mkdir(filepath.Join(root, "existing"), 0700); e != nil {
		t.Fatal(e)
	}
	if _, e := r.CreateTarget(context.Background(), protocol.ComposeCreateTarget{ID: "existing", ProjectName: "test"}); e == nil {
		t.Fatal("existing folder adopted")
	}
	outside := t.TempDir()
	if e := os.Symlink(outside, filepath.Join(root, "link")); e != nil {
		t.Fatal(e)
	}
	r.ManagedRoot = filepath.Join(root, "link", "child")
	if _, e := r.CreateTarget(context.Background(), protocol.ComposeCreateTarget{ID: "new", ProjectName: "test"}); e == nil {
		t.Fatal("symlink root accepted")
	}
	if _, e := os.Stat(filepath.Join(outside, "child")); !os.IsNotExist(e) {
		t.Fatal("wrote outside root")
	}
}
func TestManagedTargetRejectsExistingDockerProject(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux Compose runner")
	}
	root := filepath.Join(t.TempDir(), "managed")
	r := &Runner{ManagedRoot: root, runCommand: func(context.Context, string, ...string) ([]byte, error) { return []byte("existing-container\n"), nil }}
	if _, e := r.CreateTarget(context.Background(), protocol.ComposeCreateTarget{ID: "new", ProjectName: "existing"}); e == nil {
		t.Fatal("existing docker project adopted")
	}
	if _, e := os.Stat(root); !os.IsNotExist(e) {
		t.Fatal("directory created before checking Docker")
	}
}
