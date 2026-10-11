package backupprep

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPostgreSQLProducesPrivateValidatedArchiveAndCleansAttemptDirectory(t *testing.T) {
	helper := buildPostgresHelper(t)
	parent := t.TempDir()
	prepared, err := PostgreSQL(context.Background(), PostgreSQLConfig{PGDumpExecutable: helper, PGRestoreExecutable: helper, StagingDirectory: parent, Environment: map[string]string{"PGDATABASE": "fixture", "PGUSER": "fixture"}})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(prepared.Path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		t.Fatalf("prepared archive missing or empty: info=%v err=%v", info, err)
	}
	if filepath.Base(prepared.directory) == "" || filepath.Dir(prepared.directory) != parent {
		t.Fatalf("attempt directory escaped configured parent: %q", prepared.directory)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("archive permissions = %o, want 600", info.Mode().Perm())
	}
	path := prepared.Path
	if err := prepared.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("archive remains after cleanup: %v", err)
	}
}

func TestPostgreSQLRejectsPasswordAndArbitraryEnvironment(t *testing.T) {
	for _, environment := range []map[string]string{
		{"PGPASSWORD": "secret"},
		{"PGOPTIONS": "-c role=superuser"},
		{"PGSERVICE": "unreviewed-service"},
		{"PATH": "attacker-controlled"},
		{"PGUSER": "vigira"},
	} {
		if err := ValidatePostgreSQLConfig(PostgreSQLConfig{Environment: environment}); err == nil {
			t.Errorf("accepted unsafe PostgreSQL environment %v", environment)
		}
	}
	if err := ValidatePostgreSQLConfig(PostgreSQLConfig{Environment: map[string]string{"PGUSER": "vigira", "PGDATABASE": "vigira"}}); err != nil {
		t.Fatalf("rejected valid explicit connection settings: %v", err)
	}
}

func TestPostgreSQLFailureDoesNotReturnPreparedArtifact(t *testing.T) {
	helper := buildPostgresHelper(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	prepared, err := PostgreSQL(ctx, PostgreSQLConfig{PGDumpExecutable: helper, PGRestoreExecutable: helper, StagingDirectory: t.TempDir()})
	if err == nil || prepared != nil {
		t.Fatalf("canceled preparation = (%v, %v), want failure and no artifact", prepared, err)
	}
}

func buildPostgresHelper(t *testing.T) string {
	t.Helper()
	output := filepath.Join(t.TempDir(), "postgres-helper")
	if runtime.GOOS == "windows" {
		output += ".exe"
	}
	command := exec.Command("go", "build", "-o", output, "./testdata/postgres-helper")
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build PostgreSQL helper: %v: %s", err, data)
	}
	return output
}
