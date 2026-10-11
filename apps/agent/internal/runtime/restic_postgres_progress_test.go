package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/deranjer/vigara/internal/modules"
	"github.com/deranjer/vigara/internal/restic"
)

func TestPostgreSQLPreparationUsesCompatibleProgressPhases(t *testing.T) {
	pgExecutable := filepath.Join(t.TempDir(), "postgres-helper")
	if runtime.GOOS == "windows" {
		pgExecutable += ".exe"
	}
	command := exec.Command("go", "build", "-o", pgExecutable, "../backupprep/testdata/postgres-helper")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build PostgreSQL helper: %v: %s", err, output)
	}
	staging := t.TempDir()
	module := resticBackupModule{profiles: map[string]ResticProfile{"database": {
		Enabled: true, Repository: filepath.Join(t.TempDir(), "repository"), Executable: buildResticTestHelper(t),
		PostgreSQL: &PostgreSQLProfile{Enabled: true, Environment: map[string]string{"PGDATABASE": "fixture", "PGUSER": "fixture"}, PGDumpExecutable: pgExecutable, PGRestoreExecutable: pgExecutable, StagingDirectory: staging},
	}}}
	phases := []string{}
	result, err := module.ExecuteWithProgress(context.Background(), modules.JobContext{
		Configuration: map[string]any{"profileId": "database"},
		Permissions:   []modules.PermissionGrant{{Name: resticCapability, Scope: "profile:database"}},
	}, func(progress restic.Progress) error {
		// Existing v1 servers accept these phases; extending an agent's vocabulary
		// without negotiation can cancel an otherwise valid leased execution.
		switch progress.Phase {
		case "starting", "validating", "backup":
			phases = append(phases, progress.Phase)
			return nil
		default:
			return fmt.Errorf("unsupported v1 progress phase: %s", progress.Phase)
		}
	})
	if err != nil || result.Output["resticStatus"] != restic.Succeeded {
		t.Fatalf("preparation/backup failed: result=%+v err=%v phases=%v", result, err, phases)
	}
	if len(phases) < 3 || phases[0] != "validating" || phases[len(phases)-1] != "backup" {
		t.Fatalf("missing preparation-to-backup progress: %v", phases)
	}
	entries, err := os.ReadDir(staging)
	if err != nil || len(entries) != 0 {
		t.Fatalf("staged dump not cleaned: entries=%v err=%v", entries, err)
	}
}
