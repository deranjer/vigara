package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/deranjer/vigara/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	maxSnapshotsPerMinute = 10
	maxSeriesPerAgent     = 512
)

var ErrRateLimited = errors.New("telemetry ingestion rate exceeded")
var ErrSeriesLimit = errors.New("telemetry metric series limit exceeded")

// Store keeps the HTTP handler independent from the persistence backend.
type Store interface {
	Ingest(context.Context, string, []protocol.TelemetrySnapshot, time.Time) error
	Retain(context.Context, RetentionConfig, time.Time) error
}

type postgresStore struct{ pool *pgxpool.Pool }

func NewPostgresStore(pool *pgxpool.Pool) Store { return postgresStore{pool: pool} }

func (s postgresStore) Ingest(ctx context.Context, agentID string, snapshots []protocol.TelemetrySnapshot, now time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 140001))`, agentID); err != nil {
		return err
	}
	var lockedID string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM agent_identities WHERE id=$1 AND revoked_at IS NULL FOR SHARE`, agentID).Scan(&lockedID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO telemetry_ingest_limits(agent_id, window_started_at, snapshot_count) VALUES($1,$2,0) ON CONFLICT(agent_id) DO NOTHING`, agentID, now); err != nil {
		return err
	}
	var window time.Time
	var count int
	if err = tx.QueryRow(ctx, `SELECT window_started_at, snapshot_count FROM telemetry_ingest_limits WHERE agent_id=$1 FOR UPDATE`, agentID).Scan(&window, &count); err != nil {
		return err
	}
	if !now.Before(window.Add(time.Minute)) {
		window, count = now, 0
	}
	if count+len(snapshots) > maxSnapshotsPerMinute {
		return ErrRateLimited
	}
	if _, err = tx.Exec(ctx, `UPDATE telemetry_ingest_limits SET window_started_at=$2, snapshot_count=$3 WHERE agent_id=$1`, agentID, window, count+len(snapshots)); err != nil {
		return err
	}

	type inputSnapshot struct {
		ReportID   string          `json:"report_id"`
		ObservedAt time.Time       `json:"observed_at"`
		Payload    json.RawMessage `json:"payload"`
	}
	input := make([]inputSnapshot, 0, len(snapshots))
	for _, snapshot := range snapshots {
		payload, marshalErr := json.Marshal(snapshot)
		if marshalErr != nil {
			return marshalErr
		}
		input = append(input, inputSnapshot{snapshot.ID, snapshot.ObservedAt, payload})
	}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `WITH incoming AS (
			SELECT x.report_id, x.observed_at, x.payload FROM jsonb_to_recordset($2::jsonb) AS x(report_id text, observed_at timestamptz, payload jsonb)
			WHERE x.observed_at >= $3
		), dedup AS (
			INSERT INTO telemetry_report_dedup(agent_id,report_id) SELECT $1,report_id FROM incoming
			ON CONFLICT DO NOTHING RETURNING report_id
		), raw AS (
			INSERT INTO agent_telemetry(agent_id,report_id,observed_at,payload)
			SELECT $1, incoming.report_id, incoming.observed_at, incoming.payload FROM incoming JOIN dedup USING(report_id)
			ON CONFLICT(agent_id,report_id) DO NOTHING RETURNING report_id,observed_at,payload,received_at
		), latest AS (
			INSERT INTO agent_telemetry_latest(agent_id,report_id,observed_at,payload,received_at)
			SELECT $1,report_id,observed_at,payload,received_at FROM raw
			ORDER BY observed_at DESC,received_at DESC LIMIT 1
			ON CONFLICT(agent_id) DO UPDATE SET report_id=EXCLUDED.report_id,observed_at=EXCLUDED.observed_at,payload=EXCLUDED.payload,received_at=EXCLUDED.received_at
			WHERE EXCLUDED.observed_at >= agent_telemetry_latest.observed_at
			RETURNING agent_id
		)
		INSERT INTO telemetry_metric_series(agent_id,metric_name,labels)
		SELECT DISTINCT $1, metric->>'name', COALESCE(NULLIF(metric->'labels','null'::jsonb),'{}'::jsonb)
		FROM incoming JOIN raw USING(report_id) CROSS JOIN LATERAL jsonb_array_elements(COALESCE(incoming.payload->'metrics','[]'::jsonb)) AS elements(metric)
		ON CONFLICT DO NOTHING`, agentID, inputJSON, now.Add(-48*time.Hour))
	if err != nil {
		return err
	}
	var seriesCount int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM telemetry_metric_series WHERE agent_id=$1 AND NOT legacy`, agentID).Scan(&seriesCount); err != nil {
		return err
	}
	if seriesCount > maxSeriesPerAgent {
		return ErrSeriesLimit
	}
	if _, err = tx.Exec(ctx, `UPDATE devices d SET last_seen_at=$2 FROM agent_identities i WHERE i.id=$1 AND d.id=i.device_id`, agentID, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RetentionConfig controls raw history and hourly aggregate lifetime.
type RetentionConfig struct {
	RawAge    time.Duration
	RollupAge time.Duration
}

func (c RetentionConfig) Validate() error {
	if c.RawAge < 25*time.Hour || c.RawAge > 30*24*time.Hour {
		return fmt.Errorf("raw telemetry retention must be between 25h and 720h")
	}
	if c.RollupAge < c.RawAge || c.RollupAge > 365*24*time.Hour {
		return fmt.Errorf("telemetry rollup retention must be at least raw retention and at most 365d")
	}
	return nil
}
