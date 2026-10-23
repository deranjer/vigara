package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultStatePathPrefersCanonicalThenLegacyCredentials(t *testing.T) {
	userDir := t.TempDir()
	// Avoid the real machine-wide state when exercising user-profile migration.
	t.Setenv("ProgramData", t.TempDir())
	old := filepath.Join(userDir, "vigira", "agent.json")
	if err := os.MkdirAll(filepath.Dir(old), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := defaultStatePath(userDir); got != old {
		t.Fatalf("legacy credentials ignored: %s", got)
	}
	current := filepath.Join(userDir, "vigara", "agent.json")
	if err := os.MkdirAll(filepath.Dir(current), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(current, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := defaultStatePath(userDir); got != current {
		t.Fatalf("canonical path not preferred: %s", got)
	}
}
