package restic

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
	"testing"
	"time"
)

func TestManagedResticRoundTrip(t *testing.T) {
	if os.Getenv("VIGIRA_RESTIC_DRILL") != "1" {
		t.Skip("set VIGIRA_RESTIC_DRILL=1 for real Restic provisioning and downloads")
	}
	exe := os.Getenv("VIGIRA_RESTIC_EXECUTABLE")
	if exe == "" {
		exe = "restic"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	root := t.TempDir()
	repo := filepath.Join(root, "repository")
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	contents := []byte("managed backup round trip\n")
	if err := os.WriteFile(filepath.Join(source, "fixture.txt"), contents, 0600); err != nil {
		t.Fatal(err)
	}
	r := New(Config{Executable: exe, Environment: map[string]string{"RESTIC_PASSWORD": recoveryDrillPassword}})
	if err := r.InitializeMissing(ctx, repo); err != nil {
		t.Fatal("automatic initialization", err)
	}
	result := r.Backup(ctx, repo, []string{source})
	if result.Status != Succeeded || len(result.SnapshotIDs) != 1 {
		t.Fatal("backup", result)
	}
	id := result.SnapshotIDs[0]
	all, err := r.BrowseSnapshot(ctx, repo, id)
	if err != nil {
		t.Fatal(err)
	}
	var fixture string
	for _, entry := range all {
		if entry.Name == "fixture.txt" {
			fixture = entry.Path
		}
	}
	if fixture == "" {
		t.Fatal("fixture not listed")
	}
	entries, err := r.BrowseDirectory(ctx, repo, id, path.Dir(fixture))
	if err != nil || len(entries) != 1 || entries[0].Path != fixture {
		t.Fatal("directory listing", entries, err)
	}
	var file bytes.Buffer
	if err = r.DumpSnapshot(ctx, repo, id, fixture, &file, 1<<20); err != nil || !bytes.Equal(file.Bytes(), contents) {
		t.Fatal("file download", err, file.String())
	}
	var archive bytes.Buffer
	if err = r.DumpSnapshot(ctx, repo, id, path.Dir(fixture), &archive, 1<<20); err != nil {
		t.Fatal("folder download", err)
	}
	z, err := zip.NewReader(bytes.NewReader(archive.Bytes()), int64(archive.Len()))
	if err != nil {
		t.Fatal("invalid ZIP", err)
	}
	found := false
	for _, entry := range z.File {
		if path.Base(entry.Name) == "fixture.txt" {
			reader, e := entry.Open()
			if e != nil {
				t.Fatal(e)
			}
			data, e := io.ReadAll(reader)
			reader.Close()
			if e != nil || !bytes.Equal(data, contents) {
				t.Fatal("invalid ZIP contents", e)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("ZIP omitted fixture")
	}
	if err = r.DumpSnapshot(ctx, repo, id, fixture, io.Discard, 1); err == nil {
		t.Fatal("download limit ignored")
	}
	wrong := New(Config{Executable: exe, Environment: map[string]string{"RESTIC_PASSWORD": "incorrect"}})
	if err = wrong.InitializeMissing(ctx, repo); err == nil {
		t.Fatal("wrong password accepted")
	}
	if err = r.InitializeMissing(ctx, repo); err != nil {
		t.Fatal("existing repository changed", err)
	}
	populated := filepath.Join(root, "populated")
	os.Mkdir(populated, 0700)
	os.WriteFile(filepath.Join(populated, "existing-data"), contents, 0600)
	if err = r.InitializeMissing(ctx, populated); err == nil {
		t.Fatal("initialized populated destination")
	}
}
