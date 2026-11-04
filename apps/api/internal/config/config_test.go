package config

import "testing"

func TestTelemetryRetentionDefaultsAndBounds(t *testing.T) {
	// Clear both supported prefixes so local operator configuration cannot
	// change the defaults under test.
	for _, suffix := range []string{"TELEMETRY_RAW_RETENTION_HOURS", "TELEMETRY_ROLLUP_RETENTION_DAYS"} {
		t.Setenv("VIGARA_"+suffix, "")
		t.Setenv("VIGIRA_"+suffix, "")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TelemetryRawRetentionHours != 720 || cfg.TelemetryRollupRetentionDays != 365 {
		t.Fatalf("unexpected retention defaults: %+v", cfg)
	}
	for _, tc := range []struct {
		raw, rollup string
		valid       bool
	}{{"720", "365", true}, {"48", "90", true}, {"24", "365", false}, {"721", "365", false}, {"720", "29", false}, {"720", "366", false}} {
		t.Setenv("VIGARA_TELEMETRY_RAW_RETENTION_HOURS", tc.raw)
		t.Setenv("VIGARA_TELEMETRY_ROLLUP_RETENTION_DAYS", tc.rollup)
		_, err := Load()
		if (err == nil) != tc.valid {
			t.Errorf("raw=%s, rollup=%s: err=%v, want valid=%v", tc.raw, tc.rollup, err, tc.valid)
		}
	}
}
