-- Aggregate progress is attempt-scoped and contains no source or destination paths.
ALTER TABLE backup_job_attempts
    ADD COLUMN progress_sequence BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN progress_payload JSONB,
    ADD COLUMN progress_at TIMESTAMPTZ;

CREATE INDEX backup_job_attempts_progress_idx
    ON backup_job_attempts(job_id, attempt_number DESC);
