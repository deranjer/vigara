package config

import (
	"fmt"
	"strconv"

	"github.com/deranjer/vigara/apps/api/internal/mcpserver"
	"github.com/deranjer/vigara/internal/envcompat"
)

// Config contains runtime settings shared by the API server.
type Config struct {
	MCPURL                       string
	Address                      string
	DatabaseURL                  string
	SecureSessionCookie          bool
	TelemetryRawRetentionHours   int
	TelemetryRollupRetentionDays int
	AndroidBackupRoot            string
	AndroidBackupMaxFileBytes    int64
	AndroidBackupMaxTotalBytes   int64
}

func Load() (Config, error) {
	cfg := Config{
		MCPURL:                       envcompat.Getenv("VIGARA_MCP_URL"),
		Address:                      envOr("VIGIRA_ADDR", ":8080"),
		DatabaseURL:                  envOr("VIGIRA_DATABASE_URL", "postgres://vigira:vigira@localhost:5432/vigira?sslmode=disable"),
		TelemetryRawRetentionHours:   48,
		TelemetryRollupRetentionDays: 90,
		AndroidBackupRoot:            envcompat.Getenv("VIGARA_ANDROID_BACKUP_ROOT"),
		AndroidBackupMaxFileBytes:    512 << 20,
		AndroidBackupMaxTotalBytes:   100 << 30,
	}
	if cfg.MCPURL != "" {
		if err := mcpserver.ValidateURL(cfg.MCPURL); err != nil {
			return Config{}, fmt.Errorf("VIGARA_MCP_URL: %w", err)
		}
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
	if err := parseBoundedInt64("VIGIRA_ANDROID_BACKUP_MAX_FILE_BYTES", &cfg.AndroidBackupMaxFileBytes, 1<<20, 2<<30); err != nil {
		return Config{}, err
	}
	if err := parseBoundedInt64("VIGIRA_ANDROID_BACKUP_MAX_TOTAL_BYTES", &cfg.AndroidBackupMaxTotalBytes, cfg.AndroidBackupMaxFileBytes, 1<<50); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if value := envcompat.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func parseBoundedInt(key string, value *int, minimum, maximum int) error {
	raw := envcompat.Getenv(key)
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

func parseBoundedInt64(key string, value *int64, minimum, maximum int64) error {
	raw := envcompat.Getenv(key)
	if raw == "" {
		return nil
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || parsed < minimum || parsed > maximum {
		return fmt.Errorf("%s must be an integer between %d and %d", key, minimum, maximum)
	}
	*value = parsed
	return nil
}
