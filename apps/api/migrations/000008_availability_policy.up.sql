CREATE TABLE device_availability_policies (
    device_id UUID PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
    weekdays JSONB NOT NULL CHECK (jsonb_typeof(weekdays) = 'array'),
    awake_start TIME NOT NULL,
    awake_end TIME NOT NULL,
    timezone TEXT NOT NULL CHECK (length(timezone) BETWEEN 1 AND 128),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (awake_start <> awake_end)
);

CREATE TABLE device_maintenance_windows (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    device_id UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    starts_at TIMESTAMPTZ NOT NULL,
    ends_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (ends_at > starts_at),
    CHECK (ends_at <= starts_at + interval '30 days')
);
CREATE INDEX device_maintenance_windows_device_idx ON device_maintenance_windows(device_id, starts_at, ends_at);
