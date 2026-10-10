package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config contains runtime settings shared by the API server.
type Config struct {
	Address                      string
	DatabaseURL                  string
	SecureSessionCookie          bool
	TelemetryRawRetentionHours   int
	TelemetryRollupRetentionDays int
}

func Load() (Config, error) {
	cfg := Config{
		Address:                      envOr("VIGIRA_ADDR", ":8080"),
		DatabaseURL:                  envOr("VIGIRA_DATABASE_URL", "postgres://vigira:vigira@localhost:5432/vigira?sslmode=disable"),
		TelemetryRawRetentionHours:   48,
		TelemetryRollupRetentionDays: 90,
	}
	secure, err := strconv.ParseBool(envOr("VIGIRA_COOKIE_SECURE", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("VIGIRA_COOKIE_SECURE must be true or false: %w", err)
	}
	cfg.SecureSessionCookie = secure
	if err := parseBoundedInt("VIGIRA_TELEMETRY_RAW_RETENTION_HOURS", &cfg.TelemetryRawRetentionHours, 25, 168); err != nil {
		return Config{}, err
	}
	if err := parseBoundedInt("VIGIRA_TELEMETRY_ROLLUP_RETENTION_DAYS", &cfg.TelemetryRollupRetentionDays, (cfg.TelemetryRawRetentionHours+23)/24, 365); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func parseBoundedInt(key string, value *int, minimum, maximum int) error {
	raw := os.Getenv(key)
	if raw == "" {
		return nil
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < minimum || parsed > maximum {
		return fmt.Errorf("%s must be an integer between %d and %d", key, minimum, maximum)
	}
	*value = parsed
	return nil
}
