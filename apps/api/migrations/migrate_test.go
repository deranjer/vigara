package migrations

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"testing/fstest"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/jackc/pgx/v5"
)

func testDatabase(t *testing.T) (string, *pgx.Conn) {
	t.Helper()
	connectionString := os.Getenv("VIGIRA_TEST_DATABASE_URL")
	if connectionString == "" {
		t.Skip("set VIGIRA_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, connectionString)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	if _, err := admin.Exec(ctx, `BEGIN; SELECT pg_advisory_xact_lock(138139136); CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public; COMMIT;`); err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("migration_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
			t.Errorf("clean up migration test schema: %v", err)
		}
	})
	u, err := url.Parse(connectionString)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("search_path", schema+",public")
	u.RawQuery = query.Encode()
	conn, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return u.String(), conn
}

func assertVersion(t *testing.T, conn *pgx.Conn, want int, wantDirty bool) {
	t.Helper()
	var version int
	var dirty bool
	if err := conn.QueryRow(context.Background(), "SELECT version, dirty FROM schema_migrations").Scan(&version, &dirty); err != nil {
		t.Fatal(err)
	}
	if version != want || dirty != wantDirty {
		t.Fatalf("version=%d dirty=%v; want version=%d dirty=%v", version, dirty, want, wantDirty)
	}
}

func TestApplyFreshDatabaseAndRestart(t *testing.T) {
	u, conn := testDatabase(t)
	if err := Apply(u); err != nil {
		t.Fatal(err)
	}
	assertVersion(t, conn, 5, false)
	if _, err := conn.Exec(context.Background(), "INSERT INTO devices (display_name) VALUES ('keep across restart')"); err != nil {
		t.Fatal(err)
	}
	if err := Apply(u); err != nil {
		t.Fatalf("restart: %v", err)
	}
	var count int
	if err := conn.QueryRow(context.Background(), "SELECT count(*) FROM devices WHERE display_name = 'keep across restart'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("restart lost data: count=%d err=%v", count, err)
	}
	assertVersion(t, conn, 5, false)
}

func TestInitialSchemaIsConsolidated(t *testing.T) {
	entries, err := files.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 10 || entries[0].Name() != "000001_initial_schema.down.sql" || entries[1].Name() != "000001_initial_schema.up.sql" {
		t.Fatalf("unexpected migration files: %v", entries)
	}
}

func TestInitialSchemaIncludesCurrentFeaturesAndSupportsReset(t *testing.T) {
	u, conn := testDatabase(t)
	if err := Apply(u); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var tables int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM pg_tables WHERE schemaname=current_schema() AND tablename<>'schema_migrations'").Scan(&tables); err != nil || tables != 47 {
		t.Fatalf("table count=%d err=%v", tables, err)
	}
	for _, table := range []string{"stack_github_app", "stack_github_app_setups", "managed_backup_profiles", "scheduled_tasks", "mcp_oauth_clients", "telemetry_metric_rollups"} {
		var exists bool
		if err := conn.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", table).Scan(&exists); err != nil || !exists {
			t.Fatalf("missing %s: %v", table, err)
		}
	}
	var completed *time.Time
	if err := conn.QueryRow(ctx, "SELECT completed_at FROM app_setup WHERE singleton_id=1").Scan(&completed); err != nil || completed != nil {
		t.Fatalf("fresh account setup: %v %v", completed, err)
	}
	var mode string
	if err := conn.QueryRow(ctx, "INSERT INTO stack_git_settings(singleton,remote,branch) VALUES(true,'https://github.com/owner/repo.git','main') RETURNING auth_mode").Scan(&mode); err != nil || mode != "server" {
		t.Fatalf("authentication default=%q err=%v", mode, err)
	}
	// Remove later tables before exercising the original schema reset.
	updatesDown, err := files.ReadFile("000005_agent_updates.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, string(updatesDown)); err != nil {
		t.Fatal(err)
	}
	deliveryDown, err := files.ReadFile("000004_telemetry_delivery.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, string(deliveryDown)); err != nil {
		t.Fatal(err)
	}
	// Exercise the complete rollback and reinitialization, retaining the shared
	// extension and the migration runner's bookkeeping table.
	down, err := files.ReadFile("000001_initial_schema.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, string(down)); err != nil {
		t.Fatal(err)
	}
	if err = conn.QueryRow(ctx, "SELECT count(*) FROM pg_tables WHERE schemaname=current_schema() AND tablename<>'schema_migrations'").Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("rollback left %d tables: %v", tables, err)
	}
	if _, err = conn.Exec(ctx, "DELETE FROM schema_migrations"); err != nil {
		t.Fatal(err)
	}
	if err = Apply(u); err != nil {
		t.Fatalf("reinitialize: %v", err)
	}
	assertVersion(t, conn, 5, false)
}

func TestGitHubAppUpgradePreservesExistingGitSettings(t *testing.T) {
	u, conn := testDatabase(t)
	previous := fstest.MapFS{}
	entries, err := files.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if len(name) > 6 && name[:6] < "000023" && strings.HasSuffix(name, ".sql") {
			data, err := files.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			previous[name] = &fstest.MapFile{Data: data}
		}
	}
	if err = apply(previous, u); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err = conn.Exec(ctx, "INSERT INTO stack_git_settings(singleton,remote,branch,version) VALUES(true,'git@example.com:stacks.git','production',7)"); err != nil {
		t.Fatal(err)
	}
	if err = Apply(u); err != nil {
		t.Fatal(err)
	}
	var remote, branch, mode string
	var version int
	err = conn.QueryRow(ctx, "SELECT remote,branch,version,auth_mode FROM stack_git_settings WHERE singleton=true").Scan(&remote, &branch, &version, &mode)
	if err != nil || remote != "git@example.com:stacks.git" || branch != "production" || version != 7 || mode != "server" {
		t.Fatalf("existing Git settings changed: %q %q %d %q %v", remote, branch, version, mode, err)
	}
}

func TestApplyConcurrentStarts(t *testing.T) {
	u, conn := testDatabase(t)
	start := make(chan struct{})
	results := make(chan error, 4)
	for i := 0; i < cap(results); i++ {
		go func() {
			<-start
			results <- Apply(u)
		}()
	}
	close(start)
	for i := 0; i < cap(results); i++ {
		if err := <-results; err != nil {
			t.Errorf("concurrent startup: %v", err)
		}
	}
	assertVersion(t, conn, 5, false)
}

func TestApplyRejectsFailedMigration(t *testing.T) {
	u, conn := testDatabase(t)
	broken := fstest.MapFS{
		"000001_broken.up.sql": &fstest.MapFile{Data: []byte("SELECT * FROM nonexistent_migration_table;")},
	}
	if err := apply(broken, u); err == nil {
		t.Fatal("broken migration succeeded")
	}
	assertVersion(t, conn, 1, true)
	var dirty migrate.ErrDirty
	if err := Apply(u); !errors.As(err, &dirty) {
		t.Fatalf("expected dirty migration error, got %v", err)
	}
	assertVersion(t, conn, 1, true)
}

func TestApplyRejectsNewerSchema(t *testing.T) {
	u, conn := testDatabase(t)
	if err := Apply(u); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(context.Background(), "UPDATE schema_migrations SET version = 999999"); err != nil {
		t.Fatal(err)
	}
	if err := Apply(u); err == nil {
		t.Fatal("accepted a database newer than the bundled migrations")
	}
	assertVersion(t, conn, 999999, false)
}

func TestImportMigrationPreservesExistingGitSettings(t *testing.T) {
	u, conn := testDatabase(t)
	initial, err := files.ReadFile("000001_initial_schema.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	onlyInitial := fstest.MapFS{"000001_initial_schema.up.sql": &fstest.MapFile{Data: initial}}
	if err = apply(onlyInitial, u); err != nil {
		t.Fatal(err)
	}
	assertVersion(t, conn, 1, false)
	ctx := context.Background()
	if _, err = conn.Exec(ctx, "INSERT INTO stack_git_settings(singleton,remote,branch) VALUES(true,'https://github.com/owner/existing.git','main')"); err != nil {
		t.Fatal(err)
	}
	if err = Apply(u); err != nil {
		t.Fatal(err)
	}
	assertVersion(t, conn, 5, false)
	var remote string
	if err = conn.QueryRow(ctx, "SELECT remote FROM stack_git_settings WHERE singleton").Scan(&remote); err != nil || remote != "https://github.com/owner/existing.git" {
		t.Fatal("upgrade lost Git setup", remote, err)
	}
	var columns int
	if err = conn.QueryRow(ctx, "SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='stack_bindings' AND column_name IN ('recipients','imported')").Scan(&columns); err != nil || columns != 2 {
		t.Fatal("missing import metadata", columns, err)
	}
}

func TestSeriesFreshnessMigrationPreservesExistingRegistry(t *testing.T) {
	u, conn := testDatabase(t)
	initial, err := files.ReadFile("000001_initial_schema.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err = apply(fstest.MapFS{"000001_initial_schema.up.sql": &fstest.MapFile{Data: initial}}, u); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var device, agent string
	if err = conn.QueryRow(ctx, "INSERT INTO devices(display_name) VALUES('series-upgrade') RETURNING id::text").Scan(&device); err != nil {
		t.Fatal(err)
	}
	if err = conn.QueryRow(ctx, `INSERT INTO agent_identities(device_id,credential_hash,protocol_version,platform,architecture,agent_version,capabilities,allowed_capabilities) VALUES($1,decode('aabb','hex'),'v1','linux','amd64','test','[]','{}') RETURNING id::text`, device).Scan(&agent); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, `INSERT INTO telemetry_metric_series(agent_id,metric_name,labels,legacy,created_at) VALUES($1,'memory_total_bytes','{}',true,now()-interval '2 years')`, agent); err != nil {
		t.Fatal(err)
	}
	started := time.Now().Add(-time.Second)
	if err = Apply(u); err != nil {
		t.Fatal(err)
	}
	var lastObserved time.Time
	var legacy bool
	if err = conn.QueryRow(ctx, `SELECT last_observed_at,legacy FROM telemetry_metric_series WHERE agent_id=$1 AND metric_name='memory_total_bytes'`, agent).Scan(&lastObserved, &legacy); err != nil {
		t.Fatal(err)
	}
	if lastObserved.Before(started) || !legacy {
		t.Fatalf("upgrade failed to preserve old series conservatively: %s legacy=%v", lastObserved, legacy)
	}
	assertVersion(t, conn, 5, false)
}
