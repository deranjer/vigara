package notifications

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type tempError struct{ wait time.Duration }

func (e tempError) Error() string             { return "secret detail must not be persisted" }
func (e tempError) Temporary() bool           { return true }
func (e tempError) RetryAfter() time.Duration { return e.wait }
func (e tempError) ErrorCode() string         { return "remote_busy" }

type permanentError struct{}

func (permanentError) Error() string     { return "private endpoint content" }
func (permanentError) Temporary() bool   { return false }
func (permanentError) ErrorCode() string { return "invalid_destination" }

func TestRetryDelayIsBoundedAndHonorsRetryAfter(t *testing.T) {
	if got := retryDelay("discord", "event", 2, tempError{wait: MaxRetryAfter + time.Hour}); got != MaxRetryAfter {
		t.Fatalf("retry delay=%s want %s", got, MaxRetryAfter)
	}
	if got := retryDelay("d", "e", 12, errors.New("opaque")); got > MaxRetryDelay || got < MinRetryDelay {
		t.Fatalf("unknown error delay out of bounds: %s", got)
	}
}

func TestInitializeEmptyIsNoop(t *testing.T) {
	if err := Initialize(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestOutboxIntegration(t *testing.T) {
	p := notificationTestPool(t)
	ctx := context.Background()
	var device, rule, incident string
	if err := p.QueryRow(ctx, `INSERT INTO devices(display_name) VALUES('Lab gateway') RETURNING id::text`).Scan(&device); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, `INSERT INTO alert_rules(name,kind,config) VALUES('Offline check','device_unavailable','{}') RETURNING id::text`).Scan(&rule); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, `INSERT INTO alert_incidents(rule_id,dimension,status,summary,started_at) VALUES($1,$2,'firing','offline',now()) RETURNING id::text`, rule, device).Scan(&incident); err != nil {
		t.Fatal(err)
	}
	old := insertEvent(t, p, incident, "firing", time.Now().UTC().Add(-time.Minute))
	d := Destination{ID: "gotify:primary", Send: func(context.Context, Message) error { t.Fatal("historical event sent"); return nil }}
	if err := Initialize(ctx, p, []Destination{d}); err != nil {
		t.Fatal(err)
	}
	assertInt(t, p, `SELECT count(*) FROM notification_deliveries WHERE event_id=$1`, 0, old)

	newEvent := insertEvent(t, p, incident, "resolved", time.Now().UTC().Add(time.Second))
	var got Message
	calls := 0
	d.Send = func(_ context.Context, m Message) error {
		calls++
		got = m
		if calls == 1 {
			return tempError{wait: time.Hour}
		}
		return nil
	}
	worked, err := RunOneBatch(ctx, p, d)
	if err != nil || !worked {
		t.Fatalf("batch worked=%v err=%v", worked, err)
	}
	if got.DeviceName != "Lab gateway" || got.RuleName != "Offline check" || got.Summary != "condition recovered" {
		t.Fatalf("unexpected message: %+v", got)
	}
	var status, code string
	var attempts int
	var retryAt time.Time
	if err := p.QueryRow(ctx, `SELECT status,last_error_code,attempts,next_attempt_at FROM notification_deliveries WHERE event_id=$1 AND destination_id=$2`, newEvent, d.ID).Scan(&status, &code, &attempts, &retryAt); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || code != "remote_busy" || attempts != 1 || !retryAt.After(time.Now().Add(50*time.Minute)) {
		t.Fatalf("unexpected retry row %s %s %d %s", status, code, attempts, retryAt)
	}
	if err := Initialize(ctx, p, []Destination{d}); err != nil {
		t.Fatal(err)
	}
	assertInt(t, p, `SELECT count(*) FROM notification_deliveries WHERE event_id=$1 AND status='pending' AND attempts=1`, 1, newEvent)
	assertInt(t, p, `SELECT count(*) FROM notification_deliveries WHERE event_id=$1`, 0, old)

	if _, err := p.Exec(ctx, `UPDATE notification_deliveries SET status='sending',lease_token=gen_random_uuid(),lease_until=now()-interval '1 second' WHERE event_id=$1 AND destination_id=$2`, newEvent, d.ID); err != nil {
		t.Fatal(err)
	}
	d.Send = func(context.Context, Message) error { return nil }
	if _, err := RunOneBatch(ctx, p, d); err != nil {
		t.Fatal(err)
	}
	assertInt(t, p, `SELECT count(*) FROM notification_deliveries WHERE event_id=$1 AND status='delivered' AND attempts=2`, 1, newEvent)
	assertInt(t, p, `SELECT count(*) FROM alert_events WHERE id=$1 AND delivered_at IS NULL`, 1, newEvent)

	// Concurrent workers with a slow callback cannot send the same lease twice.
	second := insertEvent(t, p, incident, "resolved", time.Now().UTC().Add(2*time.Second))
	var sends atomic.Int32
	d.Send = func(context.Context, Message) error { sends.Add(1); time.Sleep(900 * time.Millisecond); return nil }
	var wg sync.WaitGroup
	var e1, e2 error
	wg.Add(2)
	go func() { defer wg.Done(); _, e1 = RunOneBatch(ctx, p, d) }()
	time.Sleep(50 * time.Millisecond)
	go func() { defer wg.Done(); _, e2 = RunOneBatch(ctx, p, d) }()
	wg.Wait()
	if e1 != nil || e2 != nil {
		t.Fatalf("concurrent errors: %v %v", e1, e2)
	}
	assertInt(t, p, `SELECT count(*) FROM notification_deliveries WHERE event_id=$1 AND status='delivered'`, 1, second)
	if sends.Load() != 1 {
		t.Fatalf("concurrent sends=%d want 1", sends.Load())
	}

	// Permanent errors are dead-lettered with a safe code only.
	if _, err := p.Exec(ctx, `UPDATE alert_events SET occurred_at=now()-CASE WHEN id=$1 THEN interval '2 seconds' ELSE interval '3 seconds' END WHERE id IN ($1,$2)`, second, newEvent); err != nil {
		t.Fatal(err)
	}
	failedEvent := insertEvent(t, p, incident, "resolved", time.Now().UTC().Add(3*time.Second))
	permanentDest := Destination{ID: "discord:permanent", Send: func(context.Context, Message) error { return permanentError{} }}
	if _, err := RunOneBatch(ctx, p, permanentDest); err != nil {
		t.Fatal(err)
	}
	var deadCode string
	if err := p.QueryRow(ctx, `SELECT status || ':' || COALESCE(last_error_code,'') || ':' || attempts::text FROM notification_deliveries WHERE event_id=$1 AND destination_id=$2`, failedEvent, permanentDest.ID).Scan(&deadCode); err != nil {
		var eventAt, activated, next time.Time
		var attempts int
		_ = p.QueryRow(ctx, `SELECT occurred_at FROM alert_events WHERE id=$1`, failedEvent).Scan(&eventAt)
		_ = p.QueryRow(ctx, `SELECT activated_at FROM notification_destinations WHERE id=$1`, permanentDest.ID).Scan(&activated)
		_ = p.QueryRow(ctx, `SELECT attempts,next_attempt_at FROM notification_deliveries WHERE event_id=$1 AND destination_id=$2`, failedEvent, permanentDest.ID).Scan(&attempts, &next)
		t.Fatalf("read failure state: %v (event=%s activated=%s next=%s attempts=%d)", err, eventAt, activated, next, attempts)
	}
	if deadCode != "dead:invalid_destination:1" {
		t.Fatalf("permanent failure state=%q", deadCode)
	}
}

func TestOrderingRetryExhaustionAndStaleLease(t *testing.T) {
	p := notificationTestPool(t)
	ctx := context.Background()
	var device, rule, incident string
	if err := p.QueryRow(ctx, `INSERT INTO devices(display_name) VALUES('Ordering test device') RETURNING id::text`).Scan(&device); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, `INSERT INTO alert_rules(name,kind,config) VALUES('Ordering test','device_unavailable','{}') RETURNING id::text`).Scan(&rule); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, `INSERT INTO alert_incidents(rule_id,dimension,status,summary,started_at) VALUES($1,$2,'firing','offline',now()) RETURNING id::text`, rule, device).Scan(&incident); err != nil {
		t.Fatal(err)
	}
	blocked := Destination{ID: "ordering:blocked", Send: func(context.Context, Message) error { return tempError{wait: time.Hour} }}
	other := Destination{ID: "ordering:other", Send: func(context.Context, Message) error { return nil }}
	if err := Initialize(ctx, p, []Destination{blocked, other}); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(time.Second)
	firing := insertEvent(t, p, incident, "firing", base)
	resolved := insertEvent(t, p, incident, "resolved", base.Add(time.Second))
	if _, err := RunOneBatch(ctx, p, blocked); err != nil {
		t.Fatal(err)
	}
	assertInt(t, p, `SELECT count(*) FROM notification_deliveries WHERE event_id=$1 AND status='pending' AND attempts=1`, 1, firing)
	assertInt(t, p, `SELECT count(*) FROM notification_deliveries WHERE event_id=$1 AND attempts=0`, 1, resolved)
	if _, err := RunOneBatch(ctx, p, other); err != nil {
		t.Fatal(err)
	}
	if _, err := RunOneBatch(ctx, p, other); err != nil {
		t.Fatal(err)
	}
	assertInt(t, p, `SELECT count(*) FROM notification_deliveries WHERE event_id IN ($1,$2) AND destination_id=$3 AND status='delivered'`, 2, firing, resolved, other.ID)
	assertInt(t, p, `SELECT count(*) FROM notification_deliveries WHERE event_id=$1 AND attempts=0`, 1, resolved)
	if _, err := p.Exec(ctx, `UPDATE alert_events SET occurred_at=now()-CASE WHEN id=$1 THEN interval '2 seconds' ELSE interval '3 seconds' END WHERE id IN ($1,$2)`, firing, resolved); err != nil {
		t.Fatal(err)
	}

	// Unknown errors retry conservatively and become dead after the bounded limit.
	var exhaustedIncident string
	if err := p.QueryRow(ctx, `INSERT INTO alert_incidents(rule_id,dimension,status,summary,started_at) VALUES($1,'retry-exhaustion','firing','offline',now()) RETURNING id::text`, rule).Scan(&exhaustedIncident); err != nil {
		t.Fatal(err)
	}
	exhausted := Destination{ID: "retry:exhaustion", Send: func(context.Context, Message) error { return errors.New("unclassified private detail") }}
	if err := Initialize(ctx, p, []Destination{exhausted}); err != nil {
		t.Fatal(err)
	}
	event := insertEvent(t, p, exhaustedIncident, "firing", time.Now().UTC().Add(time.Second))
	for i := 0; i < MaxAttempts; i++ {
		worked, err := RunOneBatch(ctx, p, exhausted)
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			t.Fatalf("retry attempt %d was not claimed", i+1)
		}
		if i+1 < MaxAttempts {
			if _, err := p.Exec(ctx, `UPDATE notification_deliveries SET next_attempt_at=now()-interval '1 second' WHERE event_id=$1 AND destination_id=$2`, event, exhausted.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	var retryState string
	if err := p.QueryRow(ctx, `SELECT status || ':' || attempts::text || ':' || COALESCE(last_error_code,'') FROM notification_deliveries WHERE event_id=$1 AND destination_id=$2`, event, exhausted.ID).Scan(&retryState); err != nil {
		t.Fatal(err)
	}
	if retryState != fmt.Sprintf("dead:%d:delivery_failed", MaxAttempts) {
		t.Fatalf("retry exhaustion state=%s", retryState)
	}
	if _, err := p.Exec(ctx, `UPDATE alert_events SET occurred_at=now()-interval '1 second' WHERE id=$1`, event); err != nil {
		t.Fatal(err)
	}

	// A stale worker's token cannot acknowledge a row after another worker
	// reclaims the expired lease.
	var leaseIncident string
	if err := p.QueryRow(ctx, `INSERT INTO alert_incidents(rule_id,dimension,status,summary,started_at) VALUES($1,'stale-lease','firing','offline',now()) RETURNING id::text`, rule).Scan(&leaseIncident); err != nil {
		t.Fatal(err)
	}
	leaseDest := Destination{ID: "lease:owner", Send: func(context.Context, Message) error { return nil }}
	if err := Initialize(ctx, p, []Destination{leaseDest}); err != nil {
		t.Fatal(err)
	}
	leaseEvent := insertEvent(t, p, leaseIncident, "firing", time.Now().UTC().Add(time.Second))
	if err := seed(ctx, p, leaseDest.ID); err != nil {
		t.Fatal(err)
	}
	first, err := claim(ctx, p, leaseDest.ID)
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim %d %v", len(first), err)
	}
	if first[0].eventID != leaseEvent {
		t.Fatalf("claimed event %s want %s", first[0].eventID, leaseEvent)
	}
	if _, err := p.Exec(ctx, `UPDATE notification_deliveries SET lease_until=now()-interval '1 second' WHERE event_id=$1 AND destination_id=$2`, leaseEvent, leaseDest.ID); err != nil {
		t.Fatal(err)
	}
	second, err := claim(ctx, p, leaseDest.ID)
	if err != nil || len(second) != 1 {
		t.Fatalf("reclaim %d %v", len(second), err)
	}
	if err := finish(ctx, p, leaseDest.ID, first[0], "delivered", time.Time{}, ""); err != nil {
		t.Fatal(err)
	}
	assertInt(t, p, `SELECT count(*) FROM notification_deliveries WHERE event_id=$1 AND status='sending' AND lease_token=$2`, 1, leaseEvent, second[0].token)
	if err := finish(ctx, p, leaseDest.ID, second[0], "delivered", time.Time{}, ""); err != nil {
		t.Fatal(err)
	}
	assertInt(t, p, `SELECT count(*) FROM notification_deliveries WHERE event_id=$1 AND status='delivered'`, 1, leaseEvent)
}

func insertEvent(t *testing.T, p *pgxpool.Pool, incident, transition string, at time.Time) string {
	t.Helper()
	var id string
	err := p.QueryRow(context.Background(), `INSERT INTO alert_events(incident_id,transition,occurred_at,payload) VALUES($1,$2,$3,jsonb_build_object('rule','Offline check','summary','condition recovered','dimension',(SELECT d.id::text FROM alert_incidents i JOIN devices d ON d.id::text=split_part(i.dimension,'/',1) WHERE i.id=$1))) RETURNING id::text`, incident, transition, at).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func assertInt(t *testing.T, p *pgxpool.Pool, q string, want int, args ...any) {
	t.Helper()
	var got int
	if err := p.QueryRow(context.Background(), q, args...).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("query returned %d want %d: %s", got, want, q)
	}
}

func notificationTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("VIGIRA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set VIGIRA_TEST_DATABASE_URL to run notification delivery integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	if _, err = admin.Exec(ctx, `BEGIN; SELECT pg_advisory_xact_lock(138139136); CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public; COMMIT;`); err != nil {
		t.Fatal(err)
	}
	schema := pgx.Identifier{fmt.Sprintf("notification_test_%d", time.Now().UnixNano())}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	for _, name := range []string{"000001_initial_schema.up.sql", "000007_alerts.up.sql", "000013_notification_delivery.up.sql"} {
		b, err := os.ReadFile("../../migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = p.Exec(ctx, string(b)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	down, err := os.ReadFile("../../migrations/000013_notification_delivery.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, string(down)); err != nil {
		t.Fatalf("roll back notification delivery migration: %v", err)
	}
	up, err := os.ReadFile("../../migrations/000013_notification_delivery.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, string(up)); err != nil {
		t.Fatalf("reapply notification delivery migration: %v", err)
	}
	return p
}
