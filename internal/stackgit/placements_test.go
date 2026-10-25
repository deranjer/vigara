package stackgit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deranjer/vigara/internal/placement"
)

const profileYAML = "# Preserve this comment\napiVersion: vigara.io/v1\nkind: ServerProfile\nprofileId: dns-primary\ndisplayName: Primary DNS\nrequirements:\n  mounts:\n    - hostPath: /mnt/appdata\n      required: true\n      purpose: Application data disk\n      persistent: true\n      requireMountedFilesystem: true\n"
const assignmentYAML = "apiVersion: vigara.io/v1\nkind: DeploymentAssignment\nassignmentId: web-primary\nstackId: web\nprofileId: dns-primary\nenvironment: production\nownership:\n  mode: exclusive\n  ownershipEpoch: 1\nenabled: false\n"

func TestPlacementsAtomicSaveConflictAndRemoval(t *testing.T) {
	r, _ := fixture(t)
	ctx := context.Background()
	if _, err := r.Save(ctx, sample("web"), "test"); err != nil {
		t.Fatal(err)
	}
	p, err := r.Placements(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	initial := p.Commit
	p.Files["profiles/dns-primary/vigara-profile.yaml"] = profileYAML
	p.Files["assignments/dns-primary/web.yaml"] = assignmentYAML
	if err = r.CheckPlacements(ctx, p); err != nil {
		t.Fatal(err)
	}
	first, err := r.SavePlacements(ctx, p, "alice")
	if err != nil || first.State != "saved" {
		t.Fatalf("save: %+v %v", first, err)
	}
	loaded, err := r.Placements(ctx, first.Commit)
	if err != nil || !strings.HasPrefix(loaded.Files["profiles/dns-primary/vigara-profile.yaml"], "# Preserve") || len(loaded.Files) != 2 {
		t.Fatalf("roundtrip: %+v %v", loaded, err)
	}
	old, err := r.Placements(ctx, initial)
	if err != nil || len(old.Files) != 0 {
		t.Fatalf("history: %+v %v", old, err)
	}
	if _, err = r.Save(ctx, sample("other"), "bob"); err != nil {
		t.Fatal(err)
	}
	loaded.Files["profiles/dns-primary/vigara-profile.yaml"] = strings.Replace(profileYAML, "Primary DNS", "Updated DNS", 1)
	second, err := r.SavePlacements(ctx, loaded, "alice")
	if err != nil || second.State != "saved" {
		t.Fatalf("unrelated stack edit conflicts: %+v %v", second, err)
	}
	if _, err = r.Read(ctx, "other", second.Commit); err != nil {
		t.Fatal("lost unrelated stack", err)
	}
	// Lost responses can be retried against the old base without another commit.
	retry, err := r.SavePlacements(ctx, loaded, "alice")
	if err != nil || retry.Commit != second.Commit {
		t.Fatalf("retry: %+v %v", retry, err)
	}
	stale := Placements{Commit: first.Commit, Files: map[string]string{"profiles/dns-primary/vigara-profile.yaml": profileYAML + "notes: Stale edit\n", "assignments/dns-primary/web.yaml": assignmentYAML}}
	if _, err = r.SavePlacements(ctx, stale, "bob"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale edit: %v", err)
	}
	current, err := r.Placements(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	delete(current.Files, "profiles/dns-primary/vigara-profile.yaml")
	if _, err = r.SavePlacements(ctx, current, "alice"); !errors.Is(err, placement.ErrInvalid) {
		t.Fatalf("dangling reference: %v", err)
	}
	delete(current.Files, "assignments/dns-primary/web.yaml")
	if _, err = r.SavePlacements(ctx, current, "alice"); err != nil {
		t.Fatal("atomic removal", err)
	}
	if _, err = r.Read(ctx, "web", ""); err != nil {
		t.Fatal("removal lost stack", err)
	}
}

func TestPlacementsValidateAgainstLatestStack(t *testing.T) {
	r, _ := fixture(t)
	ctx := context.Background()
	p, err := r.Placements(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	p.Files["profiles/dns-primary/vigara-profile.yaml"] = profileYAML
	p.Files["assignments/dns-primary/web.yaml"] = assignmentYAML
	if _, err = r.SavePlacements(ctx, p, "test"); !errors.Is(err, placement.ErrInvalid) {
		t.Fatalf("missing stack: %v", err)
	}
	if _, err = r.Save(ctx, sample("web"), "test"); err != nil {
		t.Fatal(err)
	}
	// References are checked against the latest tree while unrelated stack edits
	// are preserved, even when they were made after the editor's base commit.
	if _, err = r.SavePlacements(ctx, p, "test"); err != nil {
		t.Fatal(err)
	}
}

func TestPlacementsRejectExecutableAndUnconfirmedRevision(t *testing.T) {
	r, seed := fixture(t)
	ctx := context.Background()
	p, err := r.Placements(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	p.Files["profiles/dns-primary/vigara-profile.yaml"] = profileYAML
	saved, err := r.SavePlacements(ctx, p, "test")
	if err != nil {
		t.Fatal(err)
	}
	git(t, seed, "pull", r.Remote, "main")
	git(t, seed, "update-index", "--chmod=+x", "profiles/dns-primary/vigara-profile.yaml")
	git(t, seed, "commit", "-m", "Executable manifest")
	git(t, seed, "push", r.Remote, "main")
	if _, err = r.Placements(ctx, ""); !errors.Is(err, placement.ErrInvalid) {
		t.Fatalf("executable accepted: %v", err)
	}
	if _, err = r.Placements(ctx, saved.Commit); err != nil {
		t.Fatal("valid history unreadable", err)
	}
	git(t, seed, "checkout", "-b", "unpublished")
	git(t, seed, "commit", "--allow-empty", "-m", "Not on configured branch")
	hash := git(t, seed, "rev-parse", "HEAD")
	if _, err = r.Placements(ctx, hash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unpublished commit accepted: %v", err)
	}
}

func TestPlacementsRejectedPushAndUnsafePath(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("POSIX receive hook fixture")
	}
	r, _ := fixture(t)
	ctx := context.Background()
	p, err := r.Placements(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	p.Files["profiles/dns-primary/vigara-profile.yaml"] = profileYAML
	if err = os.WriteFile(filepath.Join(r.Remote, "hooks", "pre-receive"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	saved, err := r.SavePlacements(ctx, p, "test")
	if err != nil || saved.State != "pendingPush" {
		t.Fatalf("rejected push: %+v %v", saved, err)
	}
	if _, err = r.Placements(ctx, saved.Commit); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pending revision visible: %v", err)
	}
	p.Files["../escape.yaml"] = profileYAML
	if _, err = r.SavePlacements(ctx, p, "test"); !errors.Is(err, placement.ErrInvalid) {
		t.Fatalf("unsafe path accepted: %v", err)
	}
}
