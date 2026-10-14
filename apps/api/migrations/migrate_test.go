package migrations

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
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
	assertVersion(t, conn, 14, false)
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
	assertVersion(t, conn, 14, false)
}

func TestApplyUpgradesExistingMigrationHistory(t *testing.T) {
	u, conn := testDatabase(t)
	// The CLI/container uses the same SQL files and schema_migrations table.
	initial := fstest.MapFS{}
	entries, err := files.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "000001_") || strings.HasPrefix(entry.Name(), "000002_") {
			data, err := files.ReadFile(entry.Name())
			if err != nil {
				t.Fatal(err)
			}
			initial[entry.Name()] = &fstest.MapFile{Data: data}
		}
	}
	if err := apply(initial, u); err != nil {
		t.Fatal(err)
	}
	assertVersion(t, conn, 2, false)
	if _, err := conn.Exec(context.Background(), "INSERT INTO devices (display_name) VALUES ('keep across upgrade')"); err != nil {
		t.Fatal(err)
	}
	if err := Apply(u); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	assertVersion(t, conn, 14, false)
	var count int
	if err := conn.QueryRow(context.Background(), "SELECT count(*) FROM devices WHERE display_name = 'keep across upgrade'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("upgrade lost data: count=%d err=%v", count, err)
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
	assertVersion(t, conn, 14, false)
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
