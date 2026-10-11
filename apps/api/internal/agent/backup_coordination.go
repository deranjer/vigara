package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/deranjer/vigara/internal/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const backupLease = 2 * time.Hour

// leaseBackupJob atomically claims one due job. Exact destination strings share
// a server-side mutex; Restic's own lock remains the final guard for aliases.
func leaseBackupJob(ctx context.Context, pool *pgxpool.Pool, agentID string) (*protocol.Job, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	// Expiry is uncertainty, never permission to replay an execution.
	if _, err = tx.Exec(ctx, `UPDATE backup_job_attempts a SET outcome_unknown=true,finished_at=now() FROM backup_jobs j WHERE a.job_id=j.id AND a.finished_at IS NULL AND a.lease_expires_at<=now() AND j.status='leased'`); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE backup_jobs j SET status='unknown' WHERE j.status='leased' AND EXISTS(SELECT 1 FROM backup_job_attempts a WHERE a.job_id=j.id AND a.outcome_unknown)`); err != nil {
		return nil, err
	}
	var id, profile, token, destination string
	var scheduled time.Time
	err = tx.QueryRow(ctx, `SELECT j.id::text,j.profile_id,p.destination,j.scheduled_for,a.lease_token::text
		FROM backup_jobs j JOIN backup_policies p ON p.id=j.policy_id
		JOIN agent_identities i ON i.id=j.agent_id
		JOIN devices d ON d.id=i.device_id
		JOIN LATERAL (SELECT lease_token FROM backup_job_attempts x WHERE x.job_id=j.id ORDER BY attempt_number DESC LIMIT 1) a ON true
		WHERE j.agent_id=$1 AND j.status='leased' AND a.lease_token IS NOT NULL
		AND EXISTS(SELECT 1 FROM backup_job_attempts x WHERE x.job_id=j.id AND x.lease_expires_at>now() AND x.finished_at IS NULL)
		AND d.last_seen_at>=now()-interval '5 minutes'`, agentID).Scan(&id, &profile, &destination, &scheduled, &token)
	if err == nil {
		if err = tx.Commit(ctx); err != nil {
			return nil, err
		}
		return backupJob(id, profile, token, scheduled), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	// A held or uncertain job blocks the agent, regardless of profile.
	var blocked bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM backup_jobs WHERE agent_id=$1 AND status IN ('leased','unknown'))`, agentID).Scan(&blocked); err != nil {
		return nil, err
	}
	if blocked {
		if err = tx.Commit(ctx); err != nil {
			return nil, err
		}
		return nil, nil
	}
	err = tx.QueryRow(ctx, `SELECT j.id::text,j.profile_id,p.destination,j.scheduled_for
		FROM backup_jobs j JOIN backup_policies p ON p.id=j.policy_id
		JOIN agent_identities i ON i.id=j.agent_id JOIN devices d ON d.id=i.device_id
		WHERE j.agent_id=$1 AND j.status='pending' AND j.attempt_count<100
		AND j.policy_snapshot->'excluded_paths'='[]'::jsonb
		AND d.last_seen_at>=now()-interval '5 minutes'
		AND NOT EXISTS(SELECT 1 FROM backup_jobs x JOIN backup_policies xp ON xp.id=x.policy_id WHERE x.status IN ('leased','unknown') AND xp.destination=p.destination)
		ORDER BY j.scheduled_for,j.id FOR UPDATE OF j SKIP LOCKED LIMIT 1`, agentID).Scan(&id, &profile, &destination, &scheduled)
	if errors.Is(err, pgx.ErrNoRows) {
		if err = tx.Commit(ctx); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Advisory transaction lock serializes destination aliases at this API instance
	// boundary; the SQL exclusion above also covers other API instances.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, destination); err != nil {
		return nil, err
	}
	var occupied bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM backup_jobs j JOIN backup_policies p ON p.id=j.policy_id WHERE p.destination=$1 AND j.status IN ('leased','unknown'))`, destination).Scan(&occupied); err != nil {
		return nil, err
	}
	if occupied {
		if err = tx.Commit(ctx); err != nil {
			return nil, err
		}
		return nil, nil
	}
	err = tx.QueryRow(ctx, `UPDATE backup_jobs SET status='leased',attempt_count=attempt_count+1 WHERE id=$1 AND status='pending' RETURNING attempt_count`, id).Scan(new(int))
	if err != nil {
		return nil, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO backup_job_attempts(job_id,attempt_number,lease_expires_at) SELECT id,attempt_count,now()+($2 * interval '1 second') FROM backup_jobs WHERE id=$1 RETURNING lease_token::text`, id, int(backupLease/time.Second)).Scan(&token)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return backupJob(id, profile, token, scheduled), nil
}

func backupJob(id, profile, token string, scheduled time.Time) *protocol.Job {
	return &protocol.Job{ID: id, AttemptToken: token, ModuleID: "restic.backup", ModuleVersion: "1.0.0", Kind: "backup", CreatedAt: time.Now().UTC(), Deadline: time.Now().UTC().Add(backupLease), TimeoutSeconds: int(backupLease / time.Second), Settings: map[string]any{"profileId": profile}, Permissions: []protocol.Permission{{Name: "restic.backup", Scope: "profile:" + profile}}}
}

func recordBackupResult(ctx context.Context, tx pgx.Tx, agentID string, result protocol.JobResult) error {
	if result.JobID == "" || result.AttemptToken == "" || result.ModuleID != "restic.backup" || result.ModuleVersion != "1.0.0" || result.ID == "" {
		return errors.New("invalid backup result")
	}
	switch result.Status {
	case protocol.JobSucceeded, protocol.JobFailed, protocol.JobTimedOut, protocol.JobCancelled:
	default:
		return errors.New("invalid backup result status")
	}
	var jobAgent, profile, destination string
	err := tx.QueryRow(ctx, `SELECT j.agent_id::text,j.profile_id,p.destination FROM backup_jobs j JOIN backup_policies p ON p.id=j.policy_id WHERE j.id=$1 FOR UPDATE OF j`, result.JobID).Scan(&jobAgent, &profile, &destination)
	if err != nil {
		return err
	}
	if jobAgent != agentID {
		return errors.New("job identity mismatch")
	}
	payload, _ := json.Marshal(result)
	var attemptID string
	err = tx.QueryRow(ctx, `SELECT id::text FROM backup_job_attempts WHERE job_id=$1 AND lease_token=$2::uuid`, result.JobID, result.AttemptToken).Scan(&attemptID)
	if err != nil {
		return errors.New("attempt token invalid")
	}
	var existingID *string
	var existing []byte
	err = tx.QueryRow(ctx, `SELECT result_id,result_payload FROM backup_job_attempts WHERE id=$1 FOR UPDATE`, attemptID).Scan(&existingID, &existing)
	if err != nil {
		return err
	}
	if existingID != nil {
		var storedValue, submittedValue any
		storedOK := json.Unmarshal(existing, &storedValue) == nil
		submittedOK := json.Unmarshal(payload, &submittedValue) == nil
		if *existingID == result.ID && storedOK && submittedOK && reflect.DeepEqual(storedValue, submittedValue) {
			return nil
		}
		return errors.New("conflicting result for attempt")
	}
	unknown := result.Error != nil && result.Error.Code == "unknown_outcome"
	if _, err = tx.Exec(ctx, `UPDATE backup_job_attempts SET result_id=$2,result_payload=$3,finished_at=now(),outcome_unknown=$4,result_status=$5 WHERE id=$1`, attemptID, result.ID, payload, unknown, string(result.Status)); err != nil {
		return err
	}
	// Matching late results reconcile an unknown outcome. Keep the job terminal;
	// token remains unique and no second attempt can be issued.
	if unknown {
		_, err = tx.Exec(ctx, `UPDATE backup_jobs SET status='unknown' WHERE id=$1 AND status IN ('leased','unknown')`, result.JobID)
	} else {
		_, err = tx.Exec(ctx, `UPDATE backup_jobs SET status='completed' WHERE id=$1 AND status IN ('leased','unknown')`, result.JobID)
	}
	return err
}

func newLeaseToken() (string, error) {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return hex.EncodeToString(b), nil
}
