package notifications

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Message struct {
	EventID    string
	Transition string
	DeviceName string
	RuleName   string
	Summary    string
	OccurredAt time.Time
}

// Destination IDs are persistent identities. An absent destination is paused;
// reusing its ID resumes its prior acknowledgments and pending deliveries.
type Destination struct {
	ID   string
	Send func(context.Context, Message) error
}
type Logger interface{ Error(string, ...any) }

const (
	BatchSize     = 1
	MaxAttempts   = 12
	LeaseDuration = 45 * time.Second
	SendTimeout   = 20 * time.Second
	MinRetryDelay = 2 * time.Second
	MaxRetryDelay = 6 * time.Hour
	MaxRetryAfter = 24 * time.Hour
)

type retryable interface{ Temporary() bool }
type retryAfter interface{ RetryAfter() time.Duration }
type errorCoder interface{ ErrorCode() string }

type item struct {
	eventID, incidentID, transition, ruleName, summary, dimension, token string
	occurredAt                                                           time.Time
}

// Initialize activates each configured destination once and checks that the
// migration is present. An empty list performs no database work.
func Initialize(ctx context.Context, pool *pgxpool.Pool, destinations []Destination) error {
	if len(destinations) == 0 {
		return nil
	}
	seen := map[string]bool{}
	for _, d := range destinations {
		if d.ID == "" || len(d.ID) > 120 || d.Send == nil {
			return errors.New("invalid notification destination")
		}
		if seen[d.ID] {
			return fmt.Errorf("duplicate notification destination ID %q", d.ID)
		}
		seen[d.ID] = true
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, d := range destinations {
		if _, err = tx.Exec(ctx, `INSERT INTO notification_destinations(id,activated_at) VALUES($1,clock_timestamp()) ON CONFLICT(id) DO NOTHING`, d.ID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// Run delivers notifications concurrently per destination and exits cleanly
// when ctx is cancelled. Errors are logged with safe error codes only.
func Run(ctx context.Context, pool *pgxpool.Pool, destinations []Destination, logger Logger) {
	if len(destinations) == 0 {
		return
	}
	if err := Initialize(ctx, pool, destinations); err != nil {
		safeLog(logger, "notification destination initialization failed", "", "initialization_failed")
		return
	}
	var wg sync.WaitGroup
	for _, dest := range destinations {
		d := dest
		wg.Add(1)
		go func() { defer wg.Done(); runDestination(ctx, pool, d, logger) }()
	}
	<-ctx.Done()
	wg.Wait()
}

func runDestination(ctx context.Context, pool *pgxpool.Pool, d Destination, logger Logger) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		worked, err := RunOneBatch(ctx, pool, d)
		if err != nil && ctx.Err() == nil {
			safeLog(logger, "notification delivery batch failed", d.ID, "delivery_batch_failed")
		}
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		if worked {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// RunOneBatch can drive one bounded batch deterministically in a test or caller.
func RunOneBatch(ctx context.Context, pool *pgxpool.Pool, d Destination) (bool, error) {
	if d.ID == "" || d.Send == nil {
		return false, errors.New("invalid notification destination")
	}
	if err := Initialize(ctx, pool, []Destination{d}); err != nil {
		return false, err
	}
	if err := seed(ctx, pool, d.ID); err != nil {
		return false, err
	}
	items, err := claim(ctx, pool, d.ID)
	if err != nil || len(items) == 0 {
		return false, err
	}
	for _, it := range items {
		attemptCtx, cancel := context.WithTimeout(ctx, SendTimeout)
		msg := Message{EventID: it.eventID, Transition: it.transition, RuleName: it.ruleName, Summary: it.summary, OccurredAt: it.occurredAt}
		msg.DeviceName = deviceName(attemptCtx, pool, it.dimension)
		sendErr := d.Send(attemptCtx, msg)
		cancel()
		if sendErr == nil {
			if err := finish(ctx, pool, d.ID, it, "delivered", time.Time{}, ""); err != nil {
				return true, err
			}
			continue
		}
		if err := fail(ctx, pool, d.ID, it, sendErr); err != nil {
			return true, err
		}
	}
	return true, nil
}

func seed(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx, `INSERT INTO notification_deliveries(event_id,destination_id)
SELECT e.id,$1 FROM notification_destinations d CROSS JOIN LATERAL (
 SELECT a.id,a.occurred_at FROM alert_events a
 WHERE a.occurred_at>=d.activated_at AND NOT EXISTS (
   SELECT 1 FROM notification_deliveries nd WHERE nd.event_id=a.id AND nd.destination_id=$1)
 ORDER BY a.occurred_at,a.id LIMIT 256
) e WHERE d.id=$1 ORDER BY e.occurred_at,e.id ON CONFLICT(event_id,destination_id) DO NOTHING`, id)
	return err
}

func claim(ctx context.Context, pool *pgxpool.Pool, id string) ([]item, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `UPDATE notification_deliveries SET status='dead',lease_token=NULL,lease_until=NULL,last_error_code='attempt_limit',updated_at=now()
 WHERE destination_id=$1 AND status='sending' AND lease_until<=now() AND attempts >= $2`, id, MaxAttempts); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `WITH picked AS (
 SELECT d.event_id,d.destination_id FROM notification_deliveries d JOIN alert_events e ON e.id=d.event_id
 WHERE d.destination_id=$1 AND d.attempts<$2
 AND ((d.status='pending' AND d.next_attempt_at<=now()) OR (d.status='sending' AND d.lease_until<=now()))
 AND NOT EXISTS (SELECT 1 FROM notification_deliveries prior JOIN alert_events pe ON pe.id=prior.event_id
   WHERE prior.destination_id=d.destination_id AND pe.incident_id=e.incident_id
   AND (pe.occurred_at,pe.id)<(e.occurred_at,e.id) AND prior.status NOT IN ('delivered','dead'))
 ORDER BY e.occurred_at,e.id FOR UPDATE OF d SKIP LOCKED LIMIT $3
), leased AS (
 UPDATE notification_deliveries d SET status='sending',lease_token=gen_random_uuid(),lease_until=now()+$4::interval,
 attempts=d.attempts+1,updated_at=now() FROM picked p
 WHERE d.event_id=p.event_id AND d.destination_id=p.destination_id
 RETURNING d.event_id,d.lease_token
)
SELECT l.event_id::text,e.incident_id::text,e.transition,e.occurred_at,COALESCE(NULLIF(e.payload->>'rule',''),r.name,'Alert'),
 left(COALESCE(e.payload->>'summary',''),240),COALESCE(e.payload->>'dimension',''),l.lease_token
FROM leased l JOIN alert_events e ON e.id=l.event_id
JOIN alert_incidents i ON i.id=e.incident_id LEFT JOIN alert_rules r ON r.id=i.rule_id
ORDER BY e.occurred_at,e.id`, id, MaxAttempts, BatchSize, LeaseDuration.String())
	if err != nil {
		return nil, err
	}
	var result []item
	for rows.Next() {
		var x item
		if err := rows.Scan(&x.eventID, &x.incidentID, &x.transition, &x.occurredAt, &x.ruleName, &x.summary, &x.dimension, &x.token); err != nil {
			rows.Close()
			return nil, err
		}
		result = append(result, x)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

func finish(ctx context.Context, pool *pgxpool.Pool, id string, it item, status string, next time.Time, code string) error {
	_, err := pool.Exec(ctx, `UPDATE notification_deliveries SET status=$1,next_attempt_at=CASE WHEN $2::timestamptz IS NULL THEN next_attempt_at ELSE $2 END,
 lease_token=NULL,lease_until=NULL,last_error_code=NULLIF($3,''),updated_at=now()
 WHERE event_id=$4 AND destination_id=$5 AND status='sending' AND lease_token=$6`, status, nullTime(next), code, it.eventID, id, it.token)
	return err
}

func fail(ctx context.Context, pool *pgxpool.Pool, id string, it item, sendErr error) error {
	var attempts int
	if err := pool.QueryRow(ctx, `SELECT attempts FROM notification_deliveries WHERE event_id=$1 AND destination_id=$2 AND lease_token=$3`, it.eventID, id, it.token).Scan(&attempts); err != nil {
		return err
	}
	permanent := false
	var temp retryable
	if errors.As(sendErr, &temp) {
		permanent = !temp.Temporary()
	}
	status := "pending"
	next := time.Now().Add(retryDelay(id, it.eventID, attempts, sendErr))
	if permanent || attempts >= MaxAttempts {
		status = "dead"
		next = time.Now()
	}
	return finish(ctx, pool, id, it, status, next, errorCode(sendErr))
}

func retryDelay(destination, event string, attempt int, err error) time.Duration {
	n := max(0, min(attempt-1, 16))
	delay := time.Duration(float64(MinRetryDelay) * math.Pow(2, float64(n)))
	if delay > MaxRetryDelay {
		delay = MaxRetryDelay
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(destination + ":" + event + ":" + fmt.Sprint(attempt)))
	delay += time.Duration(h.Sum32()%1000) * delay / 5000
	if delay > MaxRetryDelay {
		delay = MaxRetryDelay
	}
	var after retryAfter
	if errors.As(err, &after) {
		ra := after.RetryAfter()
		if ra > MaxRetryAfter {
			ra = MaxRetryAfter
		}
		if ra > delay {
			delay = ra
		}
	}
	return delay
}
func errorCode(err error) string {
	var coder errorCoder
	if errors.As(err, &coder) {
		return safeCode(coder.ErrorCode())
	}
	return "delivery_failed"
}
func safeCode(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		}
		if b.Len() >= 80 {
			break
		}
	}
	if b.Len() == 0 {
		return "delivery_failed"
	}
	return b.String()
}
func safeLog(l Logger, msg, id, code string) {
	if l != nil {
		h := fnv.New32a()
		_, _ = h.Write([]byte(id))
		l.Error(msg, "destination_key", fmt.Sprintf("%08x", h.Sum32()), "error_code", code)
	}
}
func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

func deviceName(ctx context.Context, pool *pgxpool.Pool, dimension string) string {
	id := dimension
	if i := strings.IndexByte(id, '/'); i >= 0 {
		id = id[:i]
	}
	if !uuidText(id) {
		return "Device"
	}
	var name string
	err := pool.QueryRow(ctx, `SELECT d.display_name FROM devices d WHERE d.id::text=$1`, id).Scan(&name)
	if err != nil || strings.TrimSpace(name) == "" {
		return "Device"
	}
	if len([]rune(name)) > 120 {
		name = string([]rune(name)[:120])
	}
	return name
}

func uuidText(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if r != '-' {
				return false
			}
			continue
		}
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}
