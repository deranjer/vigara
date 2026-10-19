package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testAuthService(t *testing.T) (*Service, *pgxpool.Pool) {
	t.Helper()
	return testAuthServiceWithMigrations(t, "000002_local_auth.up.sql")
}

func testAuthServiceWithMigrations(t *testing.T, names ...string) (*Service, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("VIGIRA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set VIGIRA_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close(context.Background()) })
	schema := pgx.Identifier{fmt.Sprintf("auth_test_%d", time.Now().UnixNano())}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("clean up test schema: %v", err)
		}
	})
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, name := range names {
		migration, err := os.ReadFile("../../migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(migration)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	return NewService(pool, db.New(pool)), pool
}

func TestSetupAndSessionLifecycle(t *testing.T) {
	service, pool := testAuthService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if required, err := service.SetupRequired(ctx); err != nil || !required {
		t.Fatalf("fresh database: required=%v, err=%v", required, err)
	}
	const password = "correct horse battery staple"
	type outcome struct {
		session Session
		err     error
	}
	start := make(chan struct{})
	results := make(chan outcome, 4)
	for i := 0; i < cap(results); i++ {
		go func(i int) {
			<-start
			session, err := service.CreateFirstUser(ctx, fmt.Sprintf("admin-%d", i), "Administrator", password, password)
			results <- outcome{session, err}
		}(i)
	}
	close(start)
	var first Session
	winners := 0
	for i := 0; i < cap(results); i++ {
		result := <-results
		if result.err == nil {
			winners++
			first = result.session
		} else if !errors.Is(result.err, ErrSetupComplete) {
			t.Errorf("concurrent setup: %v", result.err)
		}
	}
	if winners != 1 {
		t.Fatalf("created %d first users", winners)
	}
	count, err := service.queries.CountUsers(ctx)
	if err != nil || count != 1 {
		t.Fatalf("user count=%d, err=%v", count, err)
	}
	// A new service instance must retain completed setup and the first session.
	service = NewService(pool, db.New(pool))
	if required, err := service.SetupRequired(ctx); err != nil || required {
		t.Fatalf("after restart: required=%v, err=%v", required, err)
	}
	// Completed setup is rejected even before input validation/password work.
	if _, err := service.CreateFirstUser(ctx, "!", "", "", ""); !errors.Is(err, ErrSetupComplete) {
		t.Fatalf("completed setup: %v", err)
	}
	if user, err := service.Authenticate(ctx, first.Token); err != nil || user.ID != first.User.ID {
		t.Fatalf("initial session: user=%v, err=%v", user, err)
	}
	for _, username := range []string{first.User.Username, "unknown-user"} {
		if _, err := service.Login(ctx, username, "wrong-password"); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("invalid login for %s: %v", username, err)
		}
	}
	session, err := service.Login(ctx, strings.ToUpper(first.User.Username), password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE user_sessions SET expires_at = now() - interval '1 second' WHERE token_hash = $1", hashSessionToken(first.Token)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, first.Token); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expired session was accepted: %v", err)
	}
	if user, err := service.Authenticate(ctx, session.Token); err != nil || user.ID != first.User.ID {
		t.Fatalf("login session: user=%v, err=%v", user, err)
	}
	if err := service.Logout(ctx, session.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, session.Token); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("revoked session was accepted: %v", err)
	}
}
