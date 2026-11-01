CREATE TABLE scheduled_tasks (
    id TEXT PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 80),
    enabled BOOLEAN NOT NULL DEFAULT true,
    settings JSONB NOT NULL DEFAULT '{}'::jsonb,
    interval_seconds INTEGER NOT NULL CHECK (interval_seconds > 0),
    status TEXT NOT NULL DEFAULT 'never' CHECK (status IN ('never','running','succeeded','failed')),
    last_started_at TIMESTAMPTZ,
    last_finished_at TIMESTAMPTZ,
    next_run_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error TEXT NOT NULL DEFAULT '',
    run_id UUID,
    lease_until TIMESTAMPTZ
);
