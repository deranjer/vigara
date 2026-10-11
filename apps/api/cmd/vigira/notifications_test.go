package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/alerts"
	"github.com/deranjer/vigara/apps/api/internal/notifications"
	"github.com/deranjer/vigara/apps/api/internal/notifytransport"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAlertLifecycleDeliversThroughConfiguredGotifyWithRetry(t *testing.T) {
	pool := notificationBridgePool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var mu sync.Mutex
	var messages []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/message" || r.Header.Get("X-Gotify-Key") != "test-application-token" {
			t.Errorf("invalid Gotify request method/path/authentication")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var body struct {
			Message string `json:"message"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode Gotify message: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		messages = append(messages, body.Message)
		attempt := len(messages)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if attempt == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"id":123}`)
	}))
	defer server.Close()
	t.Setenv(notifytransport.GotifyURLVar, server.URL)
	t.Setenv(notifytransport.GotifyTokenVar, "test-application-token")
	t.Setenv(notifytransport.DiscordWebhookURLVar, "")
	targets, err := notifytransport.FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	destinations := buildNotificationDestinations(targets)
	if len(destinations) != 1 || destinations[0].ID != "gotify" {
		t.Fatalf("unexpected destinations")
	}
	if err := notifications.Initialize(ctx, pool, destinations); err != nil {
		t.Fatal(err)
	}
	var deviceID string
	if err := pool.QueryRow(ctx, `INSERT INTO devices(display_name,last_seen_at) VALUES('Living room sensor',now()-interval '10 minutes') RETURNING id::text`).Scan(&deviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO alert_rules(name,kind,config) VALUES('Sensor offline','device_unavailable',$1::jsonb)`, fmt.Sprintf(`{"deviceId":%q,"staleAfterSeconds":30}`, deviceID)); err != nil {
		t.Fatal(err)
	}
	if err := alerts.Evaluate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE devices SET last_seen_at=now() WHERE id=$1`, deviceID); err != nil {
		t.Fatal(err)
	}
	if err := alerts.Evaluate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var delivered int
	for i := 0; i < 8; i++ {
		if _, err := notifications.RunOneBatch(ctx, pool, destinations[0]); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM notification_deliveries WHERE destination_id='gotify' AND status='delivered'`).Scan(&delivered); err != nil {
			t.Fatal(err)
		}
		if delivered == 2 {
			break
		}
		if _, err := pool.Exec(ctx, `UPDATE notification_deliveries SET next_attempt_at=now()-interval '1 second' WHERE status='pending'`); err != nil {
			t.Fatal(err)
		}
	}
	if delivered != 2 {
		t.Fatalf("delivered=%d, want firing and recovery", delivered)
	}
	if _, err := notifications.RunOneBatch(ctx, pool, destinations[0]); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(messages) != 3 {
		t.Fatalf("requests=%d, want failed firing, retried firing, recovery", len(messages))
	}
	for _, body := range messages {
		if !strings.Contains(body, "Living room sensor") || !strings.Contains(body, "Sensor offline") || !strings.Contains(body, "Event:") {
			t.Fatalf("notification omitted lifecycle metadata: %q", body)
		}
	}
	if !strings.Contains(messages[0], "firing") || !strings.Contains(messages[1], "firing") || !strings.Contains(messages[2], "resolved") {
		t.Fatalf("firing/recovery order incorrect: %q", messages)
	}
}

func notificationBridgePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("VIGIRA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set VIGIRA_TEST_DATABASE_URL for alert delivery integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	if _, err := admin.Exec(ctx, `BEGIN; SELECT pg_advisory_xact_lock(138139136); CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public; COMMIT;`); err != nil {
		t.Fatal(err)
	}
	schema := pgx.Identifier{fmt.Sprintf("notification_bridge_%d", time.Now().UnixNano())}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("cleanup schema: %v", err)
		}
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	migrations, err := filepath.Glob("../../migrations/*.up.sql")
	if err != nil || len(migrations) == 0 {
		t.Fatalf("migrations unavailable: %v", err)
	}
	for _, name := range migrations {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(data)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	return pool
}
