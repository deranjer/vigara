package backupprep

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestPostgreSQLRestoreDrill is an opt-in end-to-end check of the real
// preparation path. It never accepts a restore target from the environment:
// both databases are created here with unique names and dropped on exit.
func TestPostgreSQLRestoreDrill(t *testing.T) {
	adminURL := os.Getenv("VIGIRA_POSTGRES_DRILL_URL")
	if adminURL == "" {
		t.Skip("set VIGIRA_POSTGRES_DRILL_URL to run the disposable PostgreSQL dump/restore drill")
	}

	for _, tool := range []string{"pg_dump", "pg_restore"} {
		path, err := exec.LookPath(tool)
		if err != nil {
			t.Fatalf("PostgreSQL restore drill requires %s on PATH; install PostgreSQL 17 client tools", tool)
		}
		output, err := exec.Command(path, "--version").CombinedOutput()
		if err != nil {
			t.Fatalf("could not run %s --version: %v", tool, err)
		}
		version := strings.Fields(string(output))
		if len(version) < 3 || strings.Split(version[len(version)-1], ".")[0] != "17" {
			t.Fatalf("%s must be PostgreSQL 17 client tools; got %s", tool, strings.TrimSpace(string(output)))
		}
		t.Logf("%s: %s", tool, strings.TrimSpace(string(output)))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	adminConfig, err := pgx.ParseConfig(adminURL)
	if err != nil {
		t.Fatalf("parse VIGIRA_POSTGRES_DRILL_URL: %v", err)
	}
	if adminConfig.Host == "" || len(strings.Split(adminConfig.Host, ",")) != 1 {
		t.Fatal("VIGIRA_POSTGRES_DRILL_URL must use one explicit TCP host")
	}
	parsedURL, err := url.Parse(adminURL)
	if err != nil || (parsedURL.Scheme != "postgres" && parsedURL.Scheme != "postgresql") {
		t.Fatal("VIGIRA_POSTGRES_DRILL_URL must be a postgres:// URL")
	}
	if !isLoopbackHost(adminConfig.Host) || parsedURL.Query().Get("sslmode") != "disable" {
		t.Fatal("the disposable drill requires a loopback host and explicit sslmode=disable")
	}
	if adminConfig.User == "" {
		t.Fatal("VIGIRA_POSTGRES_DRILL_URL must specify a PostgreSQL user")
	}

	adminConfig.Database = "postgres"
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatalf("connect to PostgreSQL administrative database: %v", err)
	}
	t.Cleanup(func() {
		if err := admin.Close(context.Background()); err != nil {
			t.Errorf("close PostgreSQL administrative connection: %v", err)
		}
	})
	var serverVersion, serverVersionNumber string
	if err := admin.QueryRow(ctx, "SHOW server_version").Scan(&serverVersion); err != nil {
		t.Fatalf("read PostgreSQL server version: %v", err)
	}
	if err := admin.QueryRow(ctx, "SHOW server_version_num").Scan(&serverVersionNumber); err != nil {
		t.Fatalf("read PostgreSQL server version number: %v", err)
	}
	var versionNumber int
	if _, err := fmt.Sscan(serverVersionNumber, &versionNumber); err != nil || versionNumber/10000 != 17 {
		t.Fatalf("restore drill requires PostgreSQL 17 server; got %s", serverVersion)
	}
	t.Logf("PostgreSQL server: %s", serverVersion)

	suffix := fmt.Sprintf("%d_%d", time.Now().UnixNano(), os.Getpid())
	sourceDB, restoreDB := "vigira_drill_source_"+suffix, "vigira_drill_restore_"+suffix
	for _, database := range []string{sourceDB, restoreDB} {
		if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{database}.Sanitize()); err != nil {
			t.Fatalf("create disposable database: %v", err)
		}
		db := database
		t.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cleanupCancel()
			_, err := admin.Exec(cleanupCtx, "DROP DATABASE IF EXISTS "+pgx.Identifier{db}.Sanitize()+" WITH (FORCE)")
			if err != nil {
				t.Errorf("drop disposable database %s: %v", db, err)
			}
		})
	}

	sourceConfig := adminConfig.Copy()
	sourceConfig.Database = sourceDB
	source, err := pgx.ConnectConfig(ctx, sourceConfig)
	if err != nil {
		t.Fatalf("connect to disposable source database: %v", err)
	}
	if _, err := source.Exec(ctx, `CREATE TABLE public.restore_drill (
		id bigint PRIMARY KEY,
		label text NOT NULL,
		created_at timestamptz NOT NULL
	)`); err != nil {
		_ = source.Close(context.Background())
		t.Fatalf("create representative source schema: %v", err)
	}
	if _, err := source.Exec(ctx, "INSERT INTO public.restore_drill (id, label, created_at) VALUES ($1, $2, $3)", 42, "backup survives restore", time.Date(2026, 10, 11, 12, 30, 0, 0, time.UTC)); err != nil {
		_ = source.Close(context.Background())
		t.Fatalf("insert representative source row: %v", err)
	}
	if err := source.Close(context.Background()); err != nil {
		t.Fatalf("close source database connection: %v", err)
	}

	stage := t.TempDir()
	prepEnvironment, passfile := drillEnvironment(t, adminConfig, sourceDB)
	if passfile != "" {
		defer os.Remove(passfile)
	}
	prepared, err := PostgreSQL(ctx, PostgreSQLConfig{Environment: prepEnvironment, StagingDirectory: stage})
	if err != nil {
		t.Fatalf("prepare real PostgreSQL archive: %v", err)
	}
	archive := prepared.Path
	defer func() {
		if err := prepared.Cleanup(); err != nil {
			t.Errorf("clean prepared archive: %v", err)
		}
	}()

	restoreEnvironment := copyEnvironment(prepEnvironment)
	restoreEnvironment["PGDATABASE"] = restoreDB
	libpqEnvironment, err := postgresEnvironment(restoreEnvironment)
	if err != nil {
		t.Fatalf("build restore connection environment: %v", err)
	}
	if err := run(ctx, "pg_restore", libpqEnvironment, "pg_restore", "--no-owner", "--no-privileges", "--dbname", restoreDB, archive); err != nil {
		t.Fatalf("restore archive into newly created disposable database: %v", err)
	}

	restoredConfig := adminConfig.Copy()
	restoredConfig.Database = restoreDB
	restored, err := pgx.ConnectConfig(ctx, restoredConfig)
	if err != nil {
		t.Fatalf("connect to restored disposable database: %v", err)
	}
	defer restored.Close(context.Background())
	var label string
	var createdAt time.Time
	if err := restored.QueryRow(ctx, "SELECT label, created_at FROM public.restore_drill WHERE id = 42").Scan(&label, &createdAt); err != nil {
		t.Fatalf("query restored representative row: %v", err)
	}
	if label != "backup survives restore" || !createdAt.Equal(time.Date(2026, 10, 11, 12, 30, 0, 0, time.UTC)) {
		t.Fatalf("restored values = (%q, %s), data did not round-trip", label, createdAt)
	}
	if _, err := os.Stat(archive); err != nil {
		t.Fatalf("prepared archive disappeared before cleanup: %v", err)
	}
}

func drillEnvironment(t *testing.T, config *pgx.ConnConfig, database string) (map[string]string, string) {
	t.Helper()
	environment := map[string]string{
		"PGHOST": config.Host, "PGPORT": fmt.Sprint(config.Port),
		"PGUSER": config.User, "PGDATABASE": database,
		"PGSSLMODE": "disable",
	}
	if config.Password == "" {
		return environment, ""
	}
	file := filepath.Join(t.TempDir(), "pgpass")
	contents := strings.Join([]string{
		pgpassField(config.Host), pgpassField(fmt.Sprint(config.Port)), "*",
		pgpassField(config.User), pgpassField(config.Password),
	}, ":") + "\n"
	if err := os.WriteFile(file, []byte(contents), 0600); err != nil {
		t.Fatalf("write private temporary PostgreSQL password file: %v", err)
	}
	if err := os.Chmod(file, 0600); err != nil {
		t.Fatalf("protect temporary PostgreSQL password file: %v", err)
	}
	if err := securePrivatePath(file, false); err != nil {
		t.Fatalf("protect temporary PostgreSQL password file: %v", err)
	}
	environment["PGPASSFILE"] = file
	return environment, file
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func pgpassField(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	return strings.ReplaceAll(value, ":", `\:`)
}

func copyEnvironment(input map[string]string) map[string]string {
	copy := make(map[string]string, len(input))
	for key, value := range input {
		copy[key] = value
	}
	return copy
}

func TestPostgreSQLMissingClientToolFailsAndCleansAttemptDirectory(t *testing.T) {
	stage := t.TempDir()
	prepared, err := PostgreSQL(context.Background(), PostgreSQLConfig{
		Environment:      map[string]string{"PGDATABASE": "fixture", "PGUSER": "fixture"},
		PGDumpExecutable: filepath.Join(t.TempDir(), "missing-pg-dump"),
		StagingDirectory: stage,
	})
	if err == nil || prepared != nil || err.Error() != "preparation_failed" {
		t.Fatalf("missing pg_dump result = (%v, %v), want safe preparation_failed", prepared, err)
	}
	entries, readErr := os.ReadDir(stage)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("failed preparation left staging artifacts: %v", entries)
	}
}
