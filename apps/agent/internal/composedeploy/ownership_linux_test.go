package composedeploy

import (
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/deranjer/vigara/internal/protocol"
	"github.com/deranjer/vigara/internal/stack"
)

func ownershipTestOwner(t *testing.T) (*user.User, int, int) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("requires root in isolated Linux test environment to verify distinct file owners")
	}
	account, err := user.Lookup("nobody")
	if err != nil {
		t.Skip("requires existing nobody account")
	}
	uid, _ := strconv.Atoi(account.Uid)
	gid, _ := strconv.Atoi(account.Gid)
	return account, uid, gid
}

func assertOwner(t *testing.T, path string, uid, gid int) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	if int(stat.Uid) != uid || int(stat.Gid) != gid {
		t.Fatalf("%s: owner %d:%d, expected %d:%d", path, stat.Uid, stat.Gid, uid, gid)
	}
}

func TestManagedTargetOwnershipKeepsMarkerPrivate(t *testing.T) {
	account, uid, gid := ownershipTestOwner(t)
	root := filepath.Join(t.TempDir(), "managed")
	r := &Runner{ManagedRoot: root, StackOwner: account.Uid + ":" + account.Gid, runCommand: func(context.Context, string, ...string) ([]byte, error) { return nil, nil }}
	target, err := r.CreateTarget(context.Background(), protocol.ComposeCreateTarget{ID: "owned", ProjectName: "owned"})
	if err != nil {
		t.Fatal(err)
	}
	assertOwner(t, target.Directory, uid, gid)
	assertOwner(t, root, uid, gid)
	assertOwner(t, filepath.Join(target.Directory, ".vigara-target.json"), 0, 0)
	info, _ := os.Stat(target.Directory)
	if info.Mode().Perm() != 0700 {
		t.Fatal("stack folder was made public")
	}
}

func TestDeployAssignsConfigurationOwnerWithoutChangingDataOrPrivateFiles(t *testing.T) {
	account, uid, gid := ownershipTestOwner(t)
	r := testRunner(t)
	r.StackOwner = account.Username
	target := r.Targets["demo"].Directory
	data := filepath.Join(target, "data", "postgres")
	if err := os.MkdirAll(data, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(data, "database")
	if err := os.WriteFile(sentinel, []byte("keep data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(data, 12345, 12345); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(sentinel, 12345, 12345); err != nil {
		t.Fatal(err)
	}
	// An existing directory may itself be a bind source: never chown it recursively.
	configData := filepath.Join(target, "existing")
	_ = os.Mkdir(configData, 0700)
	_ = os.Chown(configData, 12345, 12345)
	s := testSnapshot()
	s.Files["config/nested/settings.yaml"] = "enabled: true\n"
	s.Files["existing/settings.yaml"] = "enabled: true\n"
	_ = os.WriteFile(filepath.Join(target, "compose.yaml"), []byte("old contents"), 0600)
	p, err := r.Plan(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.Deploy(context.Background(), Request{Job: stack.Job{ID: strings.Repeat("9", 64), Snapshot: s}, Token: p.Token, Images: p.Images})
	if err != nil || result.Status != "succeeded" {
		t.Fatalf("deploy = %+v %v", result, err)
	}
	for _, path := range []string{target, filepath.Join(target, "compose.yaml"), filepath.Join(target, "config"), filepath.Join(target, "config/nested"), filepath.Join(target, "config/nested/settings.yaml"), filepath.Join(target, "existing/settings.yaml")} {
		assertOwner(t, path, uid, gid)
	}
	for _, path := range []string{data, sentinel, configData} {
		assertOwner(t, path, 12345, 12345)
	}
	assertOwner(t, filepath.Join(target, ".vigara-jobs"), 0, 0)
	if contents, _ := os.ReadFile(sentinel); string(contents) != "keep data" {
		t.Fatal("application data changed")
	}
	for _, path := range []string{"compose.yaml", "config/nested/settings.yaml"} {
		info, _ := os.Stat(filepath.Join(target, path))
		if info.Mode().Perm() != 0600 {
			t.Fatal("configuration became public")
		}
	}
}

func TestInvalidStackOwnerFailsBeforeCreatingTarget(t *testing.T) {
	root := filepath.Join(t.TempDir(), "managed")
	for _, owner := range []string{"missing-vigara-user-123456", "root\nUser=root", "root;id", "root:missing-vigara-group-123456"} {
		r := &Runner{ManagedRoot: root, StackOwner: owner, runCommand: func(context.Context, string, ...string) ([]byte, error) {
			t.Fatal("invalid owner reached Docker")
			return nil, nil
		}}
		if _, err := r.CreateTarget(context.Background(), protocol.ComposeCreateTarget{ID: "owned", ProjectName: "owned"}); !errors.Is(err, errStackOwner) {
			t.Fatalf("owner %q = %v", owner, err)
		}
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("invalid ownership created files")
	}
}

func TestOwnershipRefusesSymlinksAndLeavesOutsideTreeUntouched(t *testing.T) {
	_, uid, gid := ownershipTestOwner(t)
	root, outside := t.TempDir(), t.TempDir()
	o := &stackOwnership{uid: uid, gid: gid}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := o.directory(root, filepath.Join(root, "link")); err == nil {
		t.Fatal("changed linked directory")
	}
	if err := o.parents(root, "link/nested/file.yaml"); err == nil {
		t.Fatal("created configuration through a link")
	}
	assertOwner(t, outside, 0, 0)
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatal("wrote outside target")
	}
}
