package main

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	webassets "github.com/deranjer/vigara/apps/api"
	"github.com/deranjer/vigara/apps/api/internal/agent"
	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/apps/api/internal/config"
	"github.com/deranjer/vigara/apps/api/internal/database"
	"github.com/deranjer/vigara/apps/api/internal/db"
	"github.com/deranjer/vigara/apps/api/internal/telemetry"
	"github.com/go-chi/chi/v5"
)

type healthOutput struct {
	Body struct {
		Status string `json:"status" example:"ok"`
	}
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid server configuration", "error", err)
		os.Exit(1)
	}
	connectCtx, cancelConnect := context.WithTimeout(context.Background(), 10*time.Second)
	database, err := database.Open(connectCtx, cfg.DatabaseURL)
	cancelConnect()
	if err != nil {
		logger.Error("database connection failed", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	queries := db.New(database.Pool)
	authService := auth.NewService(database.Pool, queries)
	readyCtx, cancelReady := context.WithTimeout(context.Background(), 5*time.Second)
	_, err = authService.SetupRequired(readyCtx)
	cancelReady()
	if err != nil {
		logger.Error("database schema is not ready; apply migrations with `task db:migrate`", "error", err)
		os.Exit(1)
	}
	agentMux := http.NewServeMux()
	agent.NewHandler(database.Pool, authService).Register(agentMux)
	telemetryMux := http.NewServeMux()
	telemetryStore := telemetry.NewPostgresStore(database.Pool)
	telemetry.NewHandler(database.Pool, telemetryStore).Register(telemetryMux)

	router := chi.NewRouter()
	router.Use(auth.NewAttemptLimiter().Middleware)
	router.Use(authService.SessionMiddleware)
	api := humachi.New(router, huma.DefaultConfig("Vigira API", "0.1.0"))
	huma.Register(api, huma.Operation{
		OperationID: "healthCheck", Method: http.MethodGet, Path: "/healthz", Summary: "Check service health",
	}, func(context.Context, *struct{}) (*healthOutput, error) {
		out := &healthOutput{}
		out.Body.Status = "ok"
		return out, nil
	})
	router.Handle("/api/agent/*", agentMux)
	router.Handle("/api/agent/telemetry", telemetryMux)
	router.Handle("/api/agents/*", agentMux)
	router.Handle("/api/devices/*", agentMux)
	auth.RegisterAPI(api, authService, cfg.SecureSessionCookie)
	staticFS, err := fs.Sub(webassets.FS, "web")
	if err != nil {
		logger.Error("web assets unavailable", "error", err)
		os.Exit(1)
	}
	router.Handle("/*", http.FileServer(http.FS(staticFS)))
	server := &http.Server{Addr: cfg.Address, Handler: router, ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	retention := telemetry.RetentionConfig{RawAge: time.Duration(cfg.TelemetryRawRetentionHours) * time.Hour, RollupAge: time.Duration(cfg.TelemetryRollupRetentionDays) * 24 * time.Hour}
	go telemetry.RunRetentionWorker(ctx, telemetryStore, retention, logger)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("server shutdown failed", "error", err)
		}
	}()
	logger.Info("starting Vigira API", "addr", cfg.Address)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
}
