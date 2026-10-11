DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM backup_jobs WHERE status = 'unknown') THEN
        RAISE EXCEPTION 'cannot remove backup coordination while unknown executions require reconciliation';
    END IF;
END $$;
DROP INDEX IF EXISTS backup_jobs_coordination_idx;
ALTER TABLE backup_job_attempts DROP CONSTRAINT IF EXISTS backup_job_attempts_result_pair_check;
ALTER TABLE backup_job_attempts DROP COLUMN IF EXISTS outcome_unknown;
ALTER TABLE backup_job_attempts DROP COLUMN IF EXISTS result_payload;
ALTER TABLE backup_job_attempts DROP COLUMN IF EXISTS result_id;
ALTER TABLE backup_jobs DROP CONSTRAINT backup_jobs_status_check;
ALTER TABLE backup_jobs ADD CONSTRAINT backup_jobs_status_check
    CHECK (status IN ('waiting', 'pending', 'leased', 'completed', 'skipped'));
