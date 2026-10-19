// Package audit records who did what to which resource. Entries are metadata
// only: they never contain secret values, command output, or keystrokes.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	OutcomeSuccess = "success"
	OutcomeFailure = "failure"
	OutcomeDenied  = "denied"
)

// Event is one auditable action. ActorID may be empty for system actions, in
// which case ActorName identifies the source.
type Event struct {
	ActorID    string
	ActorName  string
	Action     string
	TargetKind string
	TargetID   string
	AgentID    string
	Outcome    string
	Detail     map[string]any
}

// Entry is a stored event.
type Entry struct {
	ID         int64          `json:"id"`
	OccurredAt time.Time      `json:"occurredAt"`
	ActorID    string         `json:"actorId,omitempty"`
	ActorName  string         `json:"actorName"`
	Action     string         `json:"action"`
	TargetKind string         `json:"targetKind,omitempty"`
	TargetID   string         `json:"targetId,omitempty"`
	AgentID    string         `json:"agentId,omitempty"`
	Outcome    string         `json:"outcome"`
	Detail     map[string]any `json:"detail,omitempty"`
}

const maxDetailBytes = 4096

// Recorder writes and reads audit events.
type Recorder struct{ pool *pgxpool.Pool }

func NewRecorder(pool *pgxpool.Pool) *Recorder { return &Recorder{pool: pool} }

func clip(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

// Record stores an event. A nil Recorder discards it so optional wiring in
// tests and tools stays simple; callers that must not lose events should check
// the returned error.
func (r *Recorder) Record(ctx context.Context, event Event) error {
	if r == nil {
		return nil
	}
	event.Action = strings.TrimSpace(event.Action)
	if event.Action == "" || strings.TrimSpace(event.ActorName) == "" {
		return errors.New("audit event needs an action and an actor")
	}
	switch event.Outcome {
	case OutcomeSuccess, OutcomeFailure, OutcomeDenied:
	default:
		return fmt.Errorf("invalid audit outcome %q", event.Outcome)
	}
	detail := event.Detail
	if detail == nil {
		detail = map[string]any{}
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("encode audit detail: %w", err)
	}
	if len(encoded) > maxDetailBytes {
		encoded = []byte(`{"truncated":true}`)
	}
	_, err = r.pool.Exec(ctx, `INSERT INTO audit_events(actor_user_id, actor_name, action, target_kind, target_id, agent_id, outcome, detail)
VALUES (NULLIF($1,'')::uuid, $2, $3, $4, $5, NULLIF($6,'')::uuid, $7, $8::jsonb)`,
		event.ActorID, clip(event.ActorName, 128), clip(event.Action, 64), clip(event.TargetKind, 64), clip(event.TargetID, 256), event.AgentID, event.Outcome, string(encoded))
	if err != nil {
		return fmt.Errorf("record audit event: %w", err)
	}
	return nil
}

// ListFilter selects a page of entries, newest first. BeforeID is a cursor: only
// entries with a smaller ID are returned.
type ListFilter struct {
	Limit    int
	BeforeID int64
	AgentID  string
	ActorID  string
	Action   string
}

// List returns audit entries, newest first.
func (r *Recorder) List(ctx context.Context, filter ListFilter) ([]Entry, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx, `SELECT id, occurred_at, COALESCE(actor_user_id::text,''), actor_name, action, target_kind, target_id, COALESCE(agent_id::text,''), outcome, detail
FROM audit_events
WHERE ($1::bigint = 0 OR id < $1) AND ($2 = '' OR agent_id = $2::uuid) AND ($3 = '' OR actor_user_id = $3::uuid) AND ($4 = '' OR action = $4)
ORDER BY id DESC LIMIT $5`, filter.BeforeID, filter.AgentID, filter.ActorID, filter.Action, limit)
	if err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}
	defer rows.Close()
	entries := []Entry{}
	for rows.Next() {
		var entry Entry
		var detail []byte
		if err := rows.Scan(&entry.ID, &entry.OccurredAt, &entry.ActorID, &entry.ActorName, &entry.Action, &entry.TargetKind, &entry.TargetID, &entry.AgentID, &entry.Outcome, &detail); err != nil {
			return nil, fmt.Errorf("scan audit event: %w", err)
		}
		entry.OccurredAt = entry.OccurredAt.UTC()
		_ = json.Unmarshal(detail, &entry.Detail)
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}
