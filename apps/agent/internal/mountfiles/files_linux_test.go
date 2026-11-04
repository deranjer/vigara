package mountfiles

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/deranjer/vigara/internal/protocol"
)

func TestDirectoryListingUsesOpenedRootAfterRename(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(source, "config"), "opened mount")
	root, err := os.OpenRoot(source)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.Rename(source, filepath.Join(base, "moved")); err != nil {
		t.Fatal(err)
	}
	// Replace the old pathname with different metadata. A path-based Lstat
	// would report this directory rather than the file in the opened root.
	if err := os.MkdirAll(filepath.Join(source, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	result, err := listDirectory(root, protocol.DockerFilesResult{Entries: []protocol.DockerFileEntry{}})
	if err != nil || len(result.Entries) != 1 {
		t.Fatalf("renamed root: %+v, %v", result, err)
	}
	if got := result.Entries[0]; got.Name != "config" || got.Kind != "file" || got.Size != int64(len("opened mount")) {
		t.Fatalf("metadata came from stale path: %+v", got)
	}
}

func TestSpecialFileAndSymlinkReadsNeverFollowOrBlock(t *testing.T) {
	s, dir := setup(t)
	write(t, filepath.Join(dir, "target"), "secret")
	if err := os.Symlink("target", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"link", "fifo"} {
		if _, err := s.Read(context.Background(), protocol.DockerFileParams{Path: path}); err == nil {
			t.Fatalf("accepted %s", path)
		}
	}
}

func TestSavePreservesOwnership(t *testing.T) {
	s, dir := setup(t)
	name := filepath.Join(dir, "config")
	write(t, name, "before")
	before, _ := os.Stat(name)
	file, _ := s.Read(context.Background(), protocol.DockerFileParams{Path: "config"})
	if _, err := s.Write(context.Background(), protocol.DockerFileParams{Path: "config", Content: "after", Version: file.Version}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(name)
	a, b := before.Sys().(*syscall.Stat_t), after.Sys().(*syscall.Stat_t)
	if a.Uid != b.Uid || a.Gid != b.Gid {
		t.Fatal("save changed ownership")
	}
}

func TestReplacingFileWithSameContentsAndMtimeStillConflicts(t *testing.T) {
	s, dir := setup(t)
	name := filepath.Join(dir, "config")
	write(t, name, "same")
	before, _ := os.Stat(name)
	file, _ := s.Read(context.Background(), protocol.DockerFileParams{Path: "config"})
	write(t, filepath.Join(dir, "new"), "same")
	if err := os.Chtimes(filepath.Join(dir, "new"), before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "new"), name); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write(context.Background(), protocol.DockerFileParams{Path: "config", Content: "edited", Version: file.Version}); code(err) != protocol.ErrCodeConflict {
		t.Fatalf("replaced file: %v", err)
	}
}
