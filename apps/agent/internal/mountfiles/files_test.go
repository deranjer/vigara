package mountfiles

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/deranjer/vigara/internal/protocol"
)

type fixedSource string

func (s fixedSource) StorageSource(context.Context, protocol.DockerFileParams) (string, error) {
	return string(s), nil
}

func setup(t *testing.T) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	backup := filepath.Join(t.TempDir(), "private")
	return &Service{Resolver: fixedSource(dir), Writable: true, BackupDirectory: backup}, dir
}

func write(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(content), 0640); err != nil {
		t.Fatal(err)
	}
}
func code(err error) string {
	var failure *protocol.ChannelError
	if errors.As(err, &failure) {
		return failure.Code
	}
	return ""
}

func TestBrowseAndReadAreBoundedToStorage(t *testing.T) {
	s, dir := setup(t)
	write(t, filepath.Join(dir, "Caddyfile"), "service.example {\n reverse_proxy 192.168.1.2:8080\n}\n")
	if err := os.Mkdir(filepath.Join(dir, "configs"), 0750); err != nil {
		t.Fatal(err)
	}
	listing, err := s.List(context.Background(), protocol.DockerFileParams{})
	if err != nil || len(listing.Entries) != 2 || listing.Entries[0].Kind != "directory" {
		t.Fatalf("list = %+v, %v", listing, err)
	}
	file, err := s.Read(context.Background(), protocol.DockerFileParams{Path: "Caddyfile"})
	if err != nil || !strings.Contains(file.Content, "reverse_proxy") || len(file.Version) != 64 {
		t.Fatalf("read = %+v, %v", file, err)
	}
	for _, name := range []string{"../secret", "/etc/passwd", "configs/../../secret", "configs\\secret", "a\x00b"} {
		if _, err := s.Read(context.Background(), protocol.DockerFileParams{Path: name}); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
	outside := t.TempDir()
	write(t, filepath.Join(outside, "secret"), "do not disclose")
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Log("symlink test unavailable:", err)
		return
	}
	for _, name := range []string{"escape/secret", "escape"} {
		if _, err := s.Read(context.Background(), protocol.DockerFileParams{Path: name}); err == nil {
			t.Fatalf("symlink escaped: %s", name)
		}
		if _, err := s.List(context.Background(), protocol.DockerFileParams{Path: name}); err == nil {
			t.Fatalf("symlink listing escaped: %s", name)
		}
	}
}

func TestRejectsBinaryLargeAndNonRegularFiles(t *testing.T) {
	s, dir := setup(t)
	for name, content := range map[string]string{"binary": "hello\x00world", "invalid": "\xff", "large": strings.Repeat("a", protocol.MaxDockerFileBytes+1)} {
		write(t, filepath.Join(dir, name), content)
		if _, err := s.Read(context.Background(), protocol.DockerFileParams{Path: name}); err == nil {
			t.Errorf("accepted %s", name)
		}
	}
	if _, err := s.Read(context.Background(), protocol.DockerFileParams{}); err == nil {
		t.Fatal("read accepted directory")
	}
}

func TestSaveBacksUpAndPreservesSingleFileBindInode(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux host filesystem editing")
	}
	s, dir := setup(t)
	name := filepath.Join(dir, "Caddyfile")
	write(t, name, "old upstream\r\n")
	s.Resolver = fixedSource(name)
	before, _ := os.Stat(name)
	listing, err := s.List(context.Background(), protocol.DockerFileParams{})
	if err != nil || !listing.SingleFile || len(listing.Entries) != 1 {
		t.Fatalf("single-file list: %+v, %v", listing, err)
	}
	if _, err := s.Read(context.Background(), protocol.DockerFileParams{Path: "sibling"}); err == nil {
		t.Fatal("single file exposed sibling")
	}
	file, err := s.Read(context.Background(), protocol.DockerFileParams{Path: "Caddyfile"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Write(context.Background(), protocol.DockerFileParams{Path: "Caddyfile", Content: "new\n", Version: file.Version})
	if err != nil || !result.BackedUp || result.Version == file.Version {
		t.Fatalf("save = %+v, %v", result, err)
	}
	after, _ := os.Stat(name)
	if !os.SameFile(before, after) || before.Mode() != after.Mode() {
		t.Fatal("save changed inode or mode")
	}
	data, _ := os.ReadFile(name)
	if string(data) != "new\n" {
		t.Fatalf("truncation failed: %q", data)
	}
	backups, err := os.ReadDir(s.BackupDirectory)
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups: %v, %v", backups, err)
	}
	backup, _ := os.ReadFile(filepath.Join(s.BackupDirectory, backups[0].Name()))
	if string(backup) != "old upstream\r\n" {
		t.Fatalf("backup bytes = %q", backup)
	}
	backupInfo, _ := backups[0].Info()
	if backupInfo.Mode().Perm() != 0600 {
		t.Fatal("backup is not private")
	}
	second, err := s.Write(context.Background(), protocol.DockerFileParams{Path: "Caddyfile", Content: "third", Version: result.Version})
	if err != nil {
		t.Fatal(err)
	}
	backups, _ = os.ReadDir(s.BackupDirectory)
	backup, _ = os.ReadFile(filepath.Join(s.BackupDirectory, backups[0].Name()))
	if len(backups) != 1 || string(backup) != "new\n" || second.Content != "third" {
		t.Fatal("backup did not rotate to previous version")
	}
}

func TestSaveRefusesStaleVersionsAndRequiresOptIn(t *testing.T) {
	s, dir := setup(t)
	name := filepath.Join(dir, "config")
	write(t, name, "original")
	file, err := s.Read(context.Background(), protocol.DockerFileParams{Path: "config"})
	if err != nil {
		t.Fatal(err)
	}
	write(t, name, "external update")
	p := protocol.DockerFileParams{Path: "config", Content: "my changes", Version: file.Version}
	if _, err := s.Write(context.Background(), p); code(err) != protocol.ErrCodeConflict {
		t.Fatalf("stale save = %v", err)
	}
	s.Writable = false
	if _, err := s.Write(context.Background(), p); code(err) != protocol.ErrCodeFileAccess {
		t.Fatalf("disabled save = %v", err)
	}
	data, _ := os.ReadFile(name)
	if string(data) != "external update" {
		t.Fatal("rejected save modified file")
	}
}

func TestBackupFailureLeavesOriginalUntouched(t *testing.T) {
	s, dir := setup(t)
	name := filepath.Join(dir, "config")
	write(t, name, "original")
	file, _ := s.Read(context.Background(), protocol.DockerFileParams{Path: "config"})
	s.BackupDirectory = name // A regular file cannot be the backup directory.
	if _, err := s.Write(context.Background(), protocol.DockerFileParams{Path: "config", Content: "new", Version: file.Version}); err == nil {
		t.Fatal("save succeeded without backup")
	}
	data, _ := os.ReadFile(name)
	if string(data) != "original" {
		t.Fatal("backup failure modified original")
	}
}

func TestDirectoryEntryLimit(t *testing.T) {
	s, dir := setup(t)
	for i := 0; i <= protocol.MaxDockerFileEntries; i++ {
		write(t, filepath.Join(dir, fmt.Sprintf("entry-%04d", i)), "x")
	}
	listing, err := s.List(context.Background(), protocol.DockerFileParams{})
	if err != nil || !listing.Truncated || len(listing.Entries) != protocol.MaxDockerFileEntries {
		t.Fatalf("bounded listing = %d, %v, %v", len(listing.Entries), listing.Truncated, err)
	}
}
