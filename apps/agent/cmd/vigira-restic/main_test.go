package main

import (
	"os"
	"path/filepath"
	"testing"

	agentruntime "github.com/deranjer/vigara/apps/agent/internal/runtime"
)

func TestRestoreTargetMustBeEmptyAndDisjointFromSources(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	for _, path := range []string{source, target} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	profile := agentruntime.ResticProfile{Repository: filepath.Join(root, "repo"), SourcePaths: []string{source}}
	if _, err := validateTarget(target, profile); err != nil {
		t.Fatalf("valid target rejected: %v", err)
	}
	if _, err := validateTarget(root, profile); err == nil {
		t.Fatal("target containing source/repository accepted")
	}
	if err := os.WriteFile(filepath.Join(target, "existing"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := validateTarget(target, profile); err == nil {
		t.Fatal("nonempty target accepted")
	}
}

func TestRestoreTargetRejectsSymlinkAndNestedSource(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	target := filepath.Join(root, "target")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	profile := agentruntime.ResticProfile{Repository: filepath.Join(root, "repo"), SourcePaths: []string{filepath.Join(target, "source")}}
	if _, err := validateTarget(target, profile); err == nil {
		t.Fatal("target containing configured source accepted")
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := validateTarget(link, profile); err == nil {
		t.Fatal("symlink target accepted")
	}
}

func TestFullSnapshotIDRequires64LowercaseHex(t *testing.T) {
	if !fullID("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef") {
		t.Fatal("valid ID rejected")
	}
	for _, id := range []string{"abc", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789ABCDEf", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0"} {
		if fullID(id) {
			t.Fatalf("invalid ID accepted: %q", id)
		}
	}
}

func TestLocalProfileDoesNotAllowBackendSchemes(t *testing.T) {
	profile := agentruntime.ResticProfile{Enabled: true, Repository: `C:\backup`, SourcePaths: []string{`C:\data`}}
	if !validProfile(profile) {
		t.Fatal("Windows drive repository rejected")
	}
	for _, repository := range []string{"s3:bucket/repo", "rest:https://host/repo", "rclone:remote:repo", "file:/tmp/repo"} {
		profile.Repository = repository
		if validProfile(profile) {
			t.Fatalf("remote/scheme repository accepted: %q", repository)
		}
	}
}
