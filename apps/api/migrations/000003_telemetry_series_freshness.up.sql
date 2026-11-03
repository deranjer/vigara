-- Existing series have no observation timestamp. Retain them conservatively
-- for one full retention window after this upgrade; subsequent reports refresh it.
ALTER TABLE telemetry_metric_series ADD COLUMN last_observed_at TIMESTAMPTZ NOT NULL DEFAULT now();
CREATE INDEX telemetry_metric_series_observed_idx ON telemetry_metric_series(last_observed_at);
