package stackgit

import (
	"context"
	"testing"
)

func TestRemoveStackFileFromBareRepository(t *testing.T) {
	r, _ := fixture(t)
	ctx := context.Background()
	s := sample("web")
	s.Files["config/old.yaml"] = "old: configuration\n"
	first, err := r.Save(ctx, s, "test")
	if err != nil {
		t.Fatal(err)
	}
	s.Commit = first.Commit
	delete(s.Files, "config/old.yaml")
	second, err := r.Save(ctx, s, "test")
	if err != nil || second.State != "saved" {
		t.Fatalf("remove: %+v %v", second, err)
	}
	current, err := r.Read(ctx, "web", second.Commit)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := current.Files["config/old.yaml"]; ok {
		t.Fatal("removed file retained in Git")
	}
	old, err := r.Read(ctx, "web", first.Commit)
	if err != nil || old.Files["config/old.yaml"] == "" {
		t.Fatal("historical file lost", err)
	}
}

func TestRemoveWholeStack(t *testing.T) {
	r, _ := fixture(t)
	ctx := context.Background()
	first, err := r.Save(ctx, sample("web"), "test")
	if err != nil {
		t.Fatal(err)
	}
	other, err := r.Save(ctx, sample("db"), "test")
	if err != nil {
		t.Fatal(err)
	}
	removed, err := r.Remove(ctx, "web", "test")
	if err != nil || removed.State != "saved" || removed.Commit == other.Commit {
		t.Fatalf("remove: %+v %v", removed, err)
	}
	if _, err = r.Read(ctx, "web", removed.Commit); err == nil {
		t.Fatal("stack still present after removal")
	}
	if _, err = r.Read(ctx, "db", removed.Commit); err != nil {
		t.Fatal("unrelated stack lost", err)
	}
	if old, err := r.Read(ctx, "web", first.Commit); err != nil || len(old.Files) == 0 {
		t.Fatal("history lost", err)
	}
	again, err := r.Remove(ctx, "web", "test")
	if err != nil || again.State != "saved" || again.Commit != removed.Commit {
		t.Fatalf("repeat remove should be a no-op: %+v %v", again, err)
	}
}
