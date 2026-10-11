DROP INDEX IF EXISTS backup_job_attempts_progress_idx;
ALTER TABLE backup_job_attempts
    DROP COLUMN IF EXISTS progress_at,
    DROP COLUMN IF EXISTS progress_payload,
    DROP COLUMN IF EXISTS progress_sequence;
