package restic

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestListBrowseAndCheckAreBoundedAndUseFullSnapshotID(t *testing.T) {
	fake := &fakeProcess{outputs: [][]byte{
		[]byte("restic 0.19.1\n"),
		[]byte(`[{"id":"` + strings.Repeat("a", 64) + `","time":"2026-10-10T12:00:00Z","paths":["/data"],"hostname":"host"}]`),
		[]byte("restic 0.19.1\n"),
		[]byte("{\"struct_type\":\"node\",\"name\":\"file.txt\",\"path\":\"/data/file.txt\",\"size\":3}\n"),
		[]byte("restic 0.19.1\n"),
		[]byte("{\"message_type\":\"summary\",\"num_errors\":0}\n"),
	}}
	r := testRunner(fake)
	snapshots, err := r.ListSnapshots(context.Background(), "/repo")
	if err != nil || len(snapshots) != 1 || snapshots[0].Host != "host" {
		t.Fatalf("snapshots=%#v err=%v", snapshots, err)
	}
	entries, err := r.BrowseSnapshot(context.Background(), "/repo", strings.Repeat("a", 64))
	if err != nil || len(entries) != 1 || entries[0].Name != "file.txt" {
		t.Fatalf("entries=%#v err=%v", entries, err)
	}
	if err := r.CheckRepository(context.Background(), "/repo"); err != nil {
		t.Fatal(err)
	}
	if got := fake.commands[3]; !reflect.DeepEqual(got, []string{"restic", "--repo", "/repo", "ls", "--json", strings.Repeat("a", 64)}) {
		t.Fatalf("browse args=%#v", got)
	}
}

func TestBrowseRejectsShortSnapshotIDWithoutRunningRestic(t *testing.T) {
	fake := &fakeProcess{}
	_, err := testRunner(fake).BrowseSnapshot(context.Background(), "/repo", "abc")
	if err == nil || len(fake.commands) != 0 {
		t.Fatalf("err=%v commands=%d", err, len(fake.commands))
	}
}

func TestCheckAndRetentionNeverSilentlyApplyMaintenance(t *testing.T) {
	fake := &fakeProcess{outputs: [][]byte{
		[]byte("restic 0.19.1\n"),
		[]byte("[]"),
	}}
	_, err := testRunner(fake).PlanRetention(context.Background(), "/repo", 3)
	if err != nil {
		t.Fatal(err)
	}
	got := fake.commands[1]
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "forget --dry-run --json --keep-last 3") || strings.Contains(joined, "--prune") {
		t.Fatalf("unsafe plan args: %#v", got)
	}
	if _, err := testRunner(&fakeProcess{}).PlanRetention(context.Background(), "/repo", 0); err == nil {
		t.Fatal("accepted empty retention policy")
	}
}

func TestRestoreUsesExplicitFullIDAndTargetWithoutOverwrite(t *testing.T) {
	fake := &fakeProcess{outputs: [][]byte{[]byte("restic 0.19.1\n")}}
	if err := testRunner(fake).RestoreSnapshot(context.Background(), "/repo", strings.Repeat("b", 64), "D:\\recovery"); err != nil {
		t.Fatal(err)
	}
	got := fake.commands[1]
	if !reflect.DeepEqual(got, []string{"restic", "--repo", "/repo", "restore", "--target", "D:\\recovery", strings.Repeat("b", 64)}) {
		t.Fatalf("restore args=%#v", got)
	}
	if err := testRunner(&fakeProcess{}).RestoreSnapshot(context.Background(), "/repo", "short", "D:\\recovery"); err == nil {
		t.Fatal("accepted short id")
	}
}
