ALTER TABLE backup_jobs DROP CONSTRAINT backup_jobs_status_check;
ALTER TABLE backup_jobs ADD CONSTRAINT backup_jobs_status_check
    CHECK (status IN ('waiting', 'pending', 'leased', 'completed', 'skipped', 'unknown'));

ALTER TABLE backup_job_attempts ADD COLUMN result_id TEXT;
ALTER TABLE backup_job_attempts ADD COLUMN result_payload JSONB;
ALTER TABLE backup_job_attempts ADD COLUMN outcome_unknown BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE backup_job_attempts ADD CONSTRAINT backup_job_attempts_result_pair_check
    CHECK ((result_id IS NULL) = (result_payload IS NULL));

-- Once an execution lease expires, the outcome is uncertain. The job stays
-- unknown and blocks the agent/profile and repository until operator review.
CREATE INDEX backup_jobs_coordination_idx ON backup_jobs(agent_id, profile_id, status);
