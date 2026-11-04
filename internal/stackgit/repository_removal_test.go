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
