package restic

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

const recoveryDrillPassword = "vigira-disposable-restic-drill"

// TestResticRecoveryDrill exercises the managed runner against real Restic.
// Set VIGIRA_RESTIC_DRILL=1 to opt in; all repository and fixture data lives
// beneath t.TempDir and is removed when the test exits.
func TestResticRecoveryDrill(t *testing.T) {
	if os.Getenv("VIGIRA_RESTIC_DRILL") != "1" {
		t.Skip("set VIGIRA_RESTIC_DRILL=1 to run the real Restic recovery drill")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	executable := os.Getenv("VIGIRA_RESTIC_EXECUTABLE")
	if executable == "" {
		executable = "restic"
	}
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	source := filepath.Join(root, "source")
	restore := filepath.Join(root, "restore")
	if err := os.MkdirAll(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(source, "nested", "recovery-fixture.txt")
	if err := os.MkdirAll(filepath.Dir(fixture), 0o700); err != nil {
		t.Fatal(err)
	}
	firstContents := []byte("first recovery point: alpha\n")
	if err := os.WriteFile(fixture, firstContents, 0o600); err != nil {
		t.Fatal(err)
	}

	init := exec.CommandContext(ctx, executable, "--repo", repository, "init")
	init.Env = drillEnvironment()
	if output, err := init.CombinedOutput(); err != nil {
		t.Fatalf("initialize disposable repository: %v (%s)", err, strings.TrimSpace(string(output)))
	}
	runner := New(Config{Executable: executable, Environment: map[string]string{"RESTIC_PASSWORD": recoveryDrillPassword}})
	if err := runner.ValidateRepository(ctx, repository); err != nil {
		t.Fatalf("validate initialized repository: %v", err)
	}

	first := runner.Backup(ctx, repository, []string{source})
	if first.Status != Succeeded || len(first.SnapshotIDs) != 1 || len(first.SnapshotIDs[0]) != 64 {
		t.Fatalf("first backup result = %#v", first)
	}
	firstID := first.SnapshotIDs[0]

	secondContents := []byte("second recovery point: beta\n")
	if err := os.WriteFile(fixture, secondContents, 0o600); err != nil {
		t.Fatal(err)
	}
	second := runner.Backup(ctx, repository, []string{source})
	if second.Status != Succeeded || len(second.SnapshotIDs) != 1 || len(second.SnapshotIDs[0]) != 64 {
		t.Fatalf("second backup result = %#v", second)
	}
	if second.SnapshotIDs[0] == firstID {
		t.Fatal("changed fixture did not produce a distinct snapshot")
	}

	snapshots, err := runner.ListSnapshots(ctx, repository)
	if err != nil || len(snapshots) != 2 {
		t.Fatalf("list snapshots = %d entries, err=%v", len(snapshots), err)
	}
	entries, err := runner.BrowseSnapshot(ctx, repository, firstID)
	if err != nil {
		t.Fatalf("browse first snapshot: %v", err)
	}
	foundFixture := false
	for _, entry := range entries {
		if entry.Name == filepath.Base(fixture) {
			if entry.Type != "file" {
				t.Fatalf("fixture entry type = %q, want file (entry=%#v)", entry.Type, entry)
			}
			foundFixture = true
			break
		}
	}
	if !foundFixture {
		t.Fatalf("first snapshot listing omitted %q: %#v", filepath.Base(fixture), entries)
	}
	if err := runner.CheckRepository(ctx, repository); err != nil {
		t.Fatalf("check repository: %v", err)
	}

	if err := os.Mkdir(restore, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := runner.RestoreSnapshot(ctx, repository, firstID, restore); err != nil {
		t.Fatalf("restore first snapshot to fresh target: %v", err)
	}
	restored, err := findUniqueFile(restore, filepath.Base(fixture))
	if err != nil {
		t.Fatal(err)
	}
	restoredContents, err := os.ReadFile(restored)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(restoredContents) != sha256.Sum256(firstContents) {
		t.Fatalf("restored content differs from first recovery point: got %q", restoredContents)
	}

	beforeRetention := snapshotIDs(t, runner, ctx, repository)
	plan, err := runner.PlanRetention(ctx, repository, 1)
	if err != nil {
		t.Fatalf("plan retention: %v", err)
	}
	kept, removed := 0, 0
	for _, group := range plan.Groups {
		kept += len(group.Keep)
		removed += len(group.Remove)
	}
	if kept != 1 || removed != 1 {
		t.Fatalf("keep-last 1 plan = %d keep, %d remove; want one each", kept, removed)
	}
	afterRetention := snapshotIDs(t, runner, ctx, repository)
	if !sameStrings(beforeRetention, afterRetention) || len(afterRetention) != 2 {
		t.Fatalf("dry-run retention changed snapshots: before=%v after=%v", beforeRetention, afterRetention)
	}
}

func snapshotIDs(t *testing.T, runner *Runner, ctx context.Context, repository string) []string {
	t.Helper()
	snapshots, err := runner.ListSnapshots(ctx, repository)
	if err != nil {
		t.Fatalf("list snapshots: %v", err)
	}
	ids := make([]string, len(snapshots))
	for i, snapshot := range snapshots {
		ids[i] = snapshot.ID
	}
	sort.Strings(ids)
	return ids
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func findUniqueFile(root, base string) (string, error) {
	var match string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && entry.Name() == base {
			if match != "" {
				return fmt.Errorf("restore target contains more than one %q", base)
			}
			match = path
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if match == "" {
		return "", fmt.Errorf("restore target does not contain %q", base)
	}
	return match, nil
}

func drillEnvironment() []string {
	allowed := map[string]bool{"PATH": true, "SYSTEMROOT": true, "WINDIR": true, "TEMP": true, "TMP": true, "HOME": true, "USERPROFILE": true, "APPDATA": true, "LOCALAPPDATA": true}
	if runtime.GOOS != "windows" {
		allowed["TMPDIR"] = true
	}
	env := make([]string, 0, len(os.Environ())+1)
	for _, item := range os.Environ() {
		key, _, ok := strings.Cut(item, "=")
		if ok && allowed[strings.ToUpper(key)] {
			env = append(env, item)
		}
	}
	return append(env, "RESTIC_PASSWORD="+recoveryDrillPassword)
}
