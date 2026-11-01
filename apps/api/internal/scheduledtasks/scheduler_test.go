package scheduledtasks

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/jackc/pgx/v5/pgxpool"
)

func taskPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("VIGIRA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set VIGIRA_TEST_DATABASE_URL for scheduler integration tests")
	}
	admin, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("tasks_%d", time.Now().UnixNano())
	if _, err = admin.Exec(context.Background(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	ddl, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000023_scheduled_tasks.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(context.Background(), string(ddl)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	return pool
}

func startTestService(t *testing.T, pool *pgxpool.Pool, run func(context.Context, json.RawMessage) error) *Service {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s := New(pool, slog.New(slog.NewTextHandler(io.Discard, nil)), []Definition{{ID: "test", Name: "Test task", Interval: 24 * time.Hour, Timeout: time.Minute, Available: true, Run: run}})
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); s.Close() })
	return s
}

func waitTask(t *testing.T, s *Service, condition func(Task) bool) Task {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		tasks, err := s.List(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(tasks) == 1 && condition(tasks[0]) {
			return tasks[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("task did not reach expected state")
	return Task{}
}

func TestStartupRunsDespiteFutureScheduleAndDisabledManualRuns(t *testing.T) {
	pool := taskPool(t)
	var runs atomic.Int32
	run := func(context.Context, json.RawMessage) error { runs.Add(1); return nil }
	s := startTestService(t, pool, run)
	first := waitTask(t, s, func(task Task) bool { return task.Status == "succeeded" })
	if first.LastStartedAt == nil || first.LastFinishedAt == nil || first.NextRunAt == nil || time.Until(*first.NextRunAt) < 23*time.Hour {
		t.Fatal("run timestamps/schedule missing")
	}
	second := startTestService(t, pool, run)
	waitTask(t, second, func(task Task) bool {
		return task.Status == "succeeded" && task.LastStartedAt.After(*first.LastStartedAt)
	})
	if runs.Load() != 2 {
		t.Fatalf("startup runs=%d", runs.Load())
	}
	if err := second.Update(context.Background(), "test", false, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	third := startTestService(t, pool, run)
	time.Sleep(50 * time.Millisecond)
	if runs.Load() != 2 {
		t.Fatal("disabled task ran at startup")
	}
	if err := third.RunNow("test"); err != nil {
		t.Fatal(err)
	}
	waitTask(t, third, func(task Task) bool { return task.Status == "succeeded" && runs.Load() == 3 })
}

func TestManualAndStartupClaimsCannotOverlapAcrossServices(t *testing.T) {
	pool := taskPool(t)
	var runs atomic.Int32
	release := make(chan struct{})
	run := func(ctx context.Context, _ json.RawMessage) error {
		runs.Add(1)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s := startTestService(t, pool, run)
	waitTask(t, s, func(task Task) bool { return task.Status == "running" && runs.Load() == 1 })
	other := startTestService(t, pool, run)
	if err := other.RunNow("test"); err != ErrRunning {
		t.Fatalf("duplicate manual run: %v", err)
	}
	if runs.Load() != 1 {
		t.Fatal("startup duplicated an active lease")
	}
	close(release)
	waitTask(t, s, func(task Task) bool { return task.Status == "succeeded" })
}

type testAuthorizer struct{}

func (testAuthorizer) Authorize(w http.ResponseWriter, r *http.Request, p auth.Permission) (auth.User, bool) {
	if r.Header.Get("X-Test-Role") == "" {
		http.Error(w, "login required", 401)
		return auth.User{}, false
	}
	user := auth.User{Role: r.Header.Get("X-Test-Role")}
	if !user.Can(p) {
		http.Error(w, "forbidden", 403)
		return auth.User{}, false
	}
	return user, true
}

func TestTaskHTTPAuthorizationAndSettings(t *testing.T) {
	pool := taskPool(t)
	s := startTestService(t, pool, func(context.Context, json.RawMessage) error { return nil })
	waitTask(t, s, func(task Task) bool { return task.Status == "succeeded" })
	mux := http.NewServeMux()
	s.Register(mux, testAuthorizer{}, nil)
	request := func(method, path, role, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-Test-Role", role)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	for _, role := range []string{"", "viewer", "operator"} {
		for _, entry := range []struct{ method, path, body string }{{"GET", "/api/admin/scheduled-tasks", ""}, {"POST", "/api/admin/scheduled-tasks/test/run", "{}"}, {"PATCH", "/api/admin/scheduled-tasks/test", `{"enabled":false,"settings":{}}`}} {
			w := request(entry.method, entry.path, role, entry.body)
			if w.Code != 401 && w.Code != 403 {
				t.Fatalf("role %q accessed task API: %d", role, w.Code)
			}
		}
	}
	if w := request("GET", "/api/admin/scheduled-tasks", "admin", ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := request("PATCH", "/api/admin/scheduled-tasks/test", "admin", `{"enabled":false,"settings":{"unexpected":true}}`); w.Code != 400 {
		t.Fatalf("invalid settings accepted: %d", w.Code)
	}
	if w := request("PATCH", "/api/admin/scheduled-tasks/test", "admin", `{"enabled":false,"settings":{}}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := request("POST", "/api/admin/scheduled-tasks/unknown/run", "admin", "{}"); w.Code != 404 {
		t.Fatal("unknown task accepted")
	}
	if w := request("POST", "/api/admin/scheduled-tasks/test/run", "admin", "{}"); w.Code != 202 {
		t.Fatal(w.Body.String())
	}
}

func TestFailurePersistsAndRetriesSoon(t *testing.T) {
	s := startTestService(t, taskPool(t), func(context.Context, json.RawMessage) error { return fmt.Errorf("temporary failure") })
	task := waitTask(t, s, func(task Task) bool { return task.Status == "failed" })
	if task.LastError != "temporary failure" || task.NextRunAt == nil || task.LastFinishedAt == nil || task.NextRunAt.Sub(*task.LastFinishedAt) != time.Minute {
		t.Fatal("failure state/retry schedule missing")
	}
}

func TestCloseCancelsWorkAndRecordsResult(t *testing.T) {
	pool := taskPool(t)
	s := New(pool, slog.New(slog.NewTextHandler(io.Discard, nil)), []Definition{{
		ID: "test", Name: "Test task", Interval: time.Hour, Timeout: time.Minute, Available: true,
		Run: func(ctx context.Context, _ json.RawMessage) error { <-ctx.Done(); return ctx.Err() },
	}})
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	waitTask(t, s, func(task Task) bool { return task.Status == "running" })
	s.Close()
	task := waitTask(t, s, func(task Task) bool { return task.Status == "failed" })
	if task.LastFinishedAt == nil || task.LastError != context.Canceled.Error() {
		t.Fatal("shutdown did not record canceled work")
	}
}
