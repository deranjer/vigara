package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config contains runtime settings shared by the API server.
type Config struct {
	Address             string
	DatabaseURL         string
	SecureSessionCookie bool
}

func Load() (Config, error) {
	cfg := Config{
		Address:     envOr("VIGIRA_ADDR", ":8080"),
		DatabaseURL: envOr("VIGIRA_DATABASE_URL", "postgres://vigira:vigira@localhost:5432/vigira?sslmode=disable"),
	}
	secure, err := strconv.ParseBool(envOr("VIGIRA_COOKIE_SECURE", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("VIGIRA_COOKIE_SECURE must be true or false: %w", err)
	}
	cfg.SecureSessionCookie = secure
	return cfg, nil
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
