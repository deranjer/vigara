package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	webassets "github.com/deranjer/vigara/apps/api"
	"github.com/deranjer/vigara/apps/api/internal/agent"
	"github.com/deranjer/vigara/apps/api/internal/alerts"
	"github.com/deranjer/vigara/apps/api/internal/androidbackup"
	"github.com/deranjer/vigara/apps/api/internal/audit"
	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/apps/api/internal/backups"
	"github.com/deranjer/vigara/apps/api/internal/channel"
	"github.com/deranjer/vigara/apps/api/internal/config"
	"github.com/deranjer/vigara/apps/api/internal/containers"
	"github.com/deranjer/vigara/apps/api/internal/database"
	"github.com/deranjer/vigara/apps/api/internal/db"
	"github.com/deranjer/vigara/apps/api/internal/githubapp"
	"github.com/deranjer/vigara/apps/api/internal/inventoryview"
	"github.com/deranjer/vigara/apps/api/internal/mcpserver"
	"github.com/deranjer/vigara/apps/api/internal/notifications"
	"github.com/deranjer/vigara/apps/api/internal/notifytransport"
	"github.com/deranjer/vigara/apps/api/internal/scheduledtasks"
	"github.com/deranjer/vigara/apps/api/internal/stacks"
	"github.com/deranjer/vigara/apps/api/internal/telemetry"
	"github.com/deranjer/vigara/apps/api/migrations"
	"github.com/deranjer/vigara/internal/envcompat"
	"github.com/go-chi/chi/v5"
)

type healthOutput struct {
	Body struct {
		Status string `json:"status" example:"ok"`
	}
}

var version = "dev"
var revision = ""

func main() {
	envcompat.ApplyAliases()
	if len(os.Args) > 1 {
		if os.Args[1] != "host-agent-bundle" {
			os.Stderr.WriteString("Usage: vigara [host-agent-bundle OPTIONS]\n")
			os.Exit(1)
		}
		if err := hostAgentBundle(os.Args[2:], os.Stdout, os.Stderr); err != nil {
			os.Stderr.WriteString(err.Error() + "\n")
			os.Exit(1)
		}
		return
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid server configuration", "error", err)
		os.Exit(1)
	}
	transportTargets, err := notifytransport.FromEnv()
	if err != nil {
		logger.Error("invalid notification configuration", "error", err)
		os.Exit(1)
	}
	destinations := buildNotificationDestinations(transportTargets)
	connectCtx, cancelConnect := context.WithTimeout(context.Background(), 10*time.Second)
	database, err := database.Open(connectCtx, cfg.DatabaseURL)
	cancelConnect()
	if err != nil {
		logger.Error("database connection failed", "error", err)
		os.Exit(1)
	}
	defer database.Close()
	logger.Info("applying database migrations")
	if err := migrations.Apply(cfg.DatabaseURL); err != nil {
		logger.Error("database migration failed; server will not start", "error", err)
		os.Exit(1)
	}
	logger.Info("database migrations complete")

	queries := db.New(database.Pool)
	authService := auth.NewService(database.Pool, queries)
	readyCtx, cancelReady := context.WithTimeout(context.Background(), 5*time.Second)
	_, err = authService.SetupRequired(readyCtx)
	cancelReady()
	if err != nil {
		logger.Error("database schema readiness check failed", "error", err)
		os.Exit(1)
	}
	notificationCtx, cancelNotifications := context.WithTimeout(context.Background(), 5*time.Second)
	err = notifications.Initialize(notificationCtx, database.Pool, destinations)
	cancelNotifications()
	if err != nil {
		logger.Error("notification delivery initialization failed", "error", "notification_initialization_failed")
		os.Exit(1)
	}
	alertsMux := http.NewServeMux()
	alerts.NewHandler(database.Pool, authService).Register(alertsMux)
	notifications.Configure(os.Getenv("VIGIRA_PUBLIC_URL"))
	notificationHandler := notifications.NewHandler(database.Pool, authService, destinations)
	notificationHandler.RegisterActions(alertsMux)
	notificationMux := http.NewServeMux()
	notificationHandler.Register(notificationMux)
	backupMux := http.NewServeMux()
	backups.NewHandler(database.Pool, authService).Register(backupMux)
	managedBackups, err := backups.NewManager(database.Pool, os.Getenv("VIGIRA_BACKUP_MASTER_KEY"), os.Getenv("VIGIRA_RESTIC_EXECUTABLE"))
	if err != nil {
		logger.Error("invalid managed backup configuration", "error", err)
		os.Exit(1)
	}
	managedBackups.Register(backupMux, authService)
	agentMux := http.NewServeMux()
	agentHandler := agent.NewHandler(database.Pool, authService)
	agentHandler.ConfigureManagedBackups(managedBackups)
	agentHandler.ConfigureDownloads("", os.Getenv("VIGIRA_AGENT_RELEASE_DIR"))
	allowInsecureAgentHTTP := os.Getenv("VIGIRA_ALLOW_INSECURE_AGENT_HTTP") == "true"
	for _, rejected := range agentHandler.ConfigureServerURLs(os.Getenv("VIGIRA_PUBLIC_URL"), splitList(os.Getenv("VIGIRA_PUBLIC_URLS")), allowInsecureAgentHTTP) {
		logger.Warn("ignoring agent server URL", "reason", rejected.Error())
	}
	if allowInsecureAgentHTTP {
		logger.Warn("plain HTTP is allowed for agents on private-network addresses; agent credentials and telemetry are unencrypted on the wire")
	}
	agentHandler.Register(agentMux)
	auditRecorder := audit.NewRecorder(database.Pool)
	channelHub := channel.NewHub(channel.Options{AuthenticateAgent: agentHandler.AuthenticateAgentRequest, Capabilities: agentHandler.AgentCapabilities})
	channelMux := http.NewServeMux()
	channelHub.RegisterAPI(channelMux, authService)
	containersMux := http.NewServeMux()
	containerConfig := containers.Config{Hub: channelHub, Authz: authService, Audit: auditRecorder, Names: func(ctx context.Context) (map[string]string, error) {
		rows, err := database.Pool.Query(ctx, `SELECT ai.id::text, d.display_name FROM agent_identities ai JOIN devices d ON d.id = ai.device_id WHERE ai.revoked_at IS NULL`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		names := map[string]string{}
		for rows.Next() {
			var id, name string
			if err := rows.Scan(&id, &name); err != nil {
				return nil, err
			}
			names[id] = name
		}
		return names, rows.Err()
	}}
	containers.RegisterAPI(containersMux, containerConfig)
	stackRepository, stackTargets, err := stacks.FromEnv()
	if err != nil {
		logger.Error("invalid stack configuration")
		os.Exit(1)
	}
	stackMux := http.NewServeMux()
	githubClient, err := githubapp.New(os.Getenv("VIGIRA_GITHUB_APP_MASTER_KEY"))
	if err != nil {
		logger.Error("invalid GitHub App configuration", "error", err)
		os.Exit(1)
	}
	stackAPI := stacks.Register(stackMux, stacks.Config{Agents: channelHub.Agents, GitHub: githubClient, PublicURL: os.Getenv("VIGIRA_PUBLIC_URL"), Repository: stackRepository, Targets: stackTargets, SOPS: os.Getenv("VIGIRA_SOPS_BINARY"), CentralAgeIdentity: os.Getenv("VIGIRA_COMPOSE_EDITOR_AGE_IDENTITY"), Pool: database.Pool, Hub: channelHub, Authz: authService, Audit: auditRecorder})
	adminMux := http.NewServeMux()
	authService.RegisterAdmin(adminMux, auditRecorder)
	androidBackupMux := http.NewServeMux()
	androidbackup.NewHandler(database.Pool, authService, cfg.AndroidBackupRoot, cfg.AndroidBackupMaxFileBytes, cfg.AndroidBackupMaxTotalBytes).Register(agentMux, androidBackupMux)
	telemetryMux := http.NewServeMux()
	telemetryStore := telemetry.NewPostgresStore(database.Pool)
	telemetry.NewHandler(database.Pool, telemetryStore).Register(telemetryMux)
	inventoryMux := http.NewServeMux()
	inventoryview.NewHandler(database.Pool, authService).Register(inventoryMux)

	router := chi.NewRouter()
	if info, ok := debug.ReadBuildInfo(); ok {
		if version == "dev" && info.Main.Version != "" && info.Main.Version != "(devel)" {
			version = info.Main.Version
		}
		if revision == "" {
			for _, setting := range info.Settings {
				if setting.Key == "vcs.revision" {
					revision = setting.Value
				}
			}
		}
	}
	if err := agentHandler.ConfigureReleaseSync(os.Getenv("VIGIRA_AGENT_RELEASE_CACHE"), os.Getenv("VIGIRA_AGENT_RELEASE_TAG"), revision); err != nil {
		logger.Warn("automatic agent release downloads disabled", "reason", err.Error())
	}
	router.Use(auth.NewAttemptLimiter().Middleware)
	router.Use(authService.SessionMiddleware)
	router.Use(authService.WriteGuard)
	router.Get("/api/mcp/status", mcpserver.StatusHandler(authService, cfg.MCPURL))
	var mcpHandler *mcpserver.Handler
	if cfg.MCPURL != "" {
		mcpHandler, err = mcpserver.New(database.Pool, authService, auditRecorder, cfg.MCPURL)
		if err != nil {
			logger.Error("invalid MCP configuration", "error", err)
			os.Exit(1)
		}
		mcpMux := http.NewServeMux()
		mcpHandler.ConfigureManagement(stackAPI.Config, containerConfig)
		mcpHandler.Register(mcpMux)
		router.Handle("/mcp", mcpMux)
		router.Handle("/oauth/*", mcpMux)
		router.Handle("/.well-known/oauth-protected-resource", mcpMux)
		router.Handle("/.well-known/oauth-protected-resource/mcp", mcpMux)
		router.Handle("/.well-known/oauth-authorization-server", mcpMux)
		logger.Info("MCP enabled", "url", strings.TrimSuffix(cfg.MCPURL, "/")+"/mcp")
	}
	router.Get("/api/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"version": version, "revision": revision})
	})
	api := humachi.New(router, huma.DefaultConfig("Vigara API", "0.1.0"))
	huma.Register(api, huma.Operation{
		OperationID: "healthCheck", Method: http.MethodGet, Path: "/healthz", Summary: "Check service health",
	}, func(context.Context, *struct{}) (*healthOutput, error) {
		out := &healthOutput{}
		out.Body.Status = "ok"
		return out, nil
	})
	router.Handle("/api/agent/channel", http.HandlerFunc(channelHub.ServeAgent))
	router.Handle("/api/agent/*", agentMux)
	router.Handle("/api/channel/*", channelMux)
	router.Handle("/api/containers", containersMux)
	router.Handle("/api/containers/*", containersMux)
	router.Handle("/api/docker/*", containersMux)
	router.Handle("/api/image-updates", stackMux)
	router.Handle("/api/image-updates/*", stackMux)
	router.Handle("/api/stacks", stackMux)
	router.Handle("/api/stacks/*", stackMux)
	router.Handle("/api/stack-placements", stackMux)
	router.Handle("/api/stack-placements/*", stackMux)
	router.Handle("/api/users", adminMux)
	router.Handle("/api/users/*", adminMux)
	router.Handle("/api/audit", adminMux)
	router.Handle("/api/agent/telemetry", telemetryMux)
	router.Handle("/api/agent/backups/status", alertsMux)
	router.Handle("/api/backups/android/*", androidBackupMux)
	router.Handle("/api/backups/*", backupMux)
	router.Handle("/api/alerts/*", alertsMux)
	router.Handle("/api/notifications", notificationMux)
	router.Handle("/api/notifications/*", notificationMux)
	router.Handle("/api/devices/{deviceID}/availability", alertsMux)
	router.Handle("/api/devices/{deviceID}/maintenance", alertsMux)
	router.Handle("/api/devices/{deviceID}/maintenance/{windowID}", alertsMux)
	router.Handle("/api/agents/*", agentMux)
	router.Handle("/api/agent-downloads", agentMux)
	router.Handle("/api/agent-downloads/*", agentMux)
	router.Handle("/api/devices/*", agentMux)
	router.Handle("/api/inventory/*", inventoryMux)
	auth.RegisterAPI(api, authService, cfg.SecureSessionCookie, auditRecorder)
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
	tasks := scheduledtasks.New(database.Pool, logger, []scheduledtasks.Definition{
		{ID: "agent-bundles", Name: "Scan agent bundles", Description: "Download the newest compatible published agent bundles and apply cache retention.", Interval: 24 * time.Hour, Timeout: 30 * time.Minute, Available: agentHandler.ReleaseSyncEnabled(), Defaults: json.RawMessage(`{"purgeOld":true,"keepVersions":2}`),
			Validate: func(raw json.RawMessage) error { _, err := agent.ParseReleaseRetentionSettings(raw); return err },
			Run: func(ctx context.Context, raw json.RawMessage) error {
				settings, err := agent.ParseReleaseRetentionSettings(raw)
				if err != nil {
					return err
				}
				return agentHandler.RefreshReleases(ctx, settings.PurgeOld, settings.KeepVersions)
			}},
		{ID: "telemetry-retention", Name: "Telemetry retention", Description: "Roll up expired raw telemetry and purge expired history.", Interval: time.Minute, Timeout: 55 * time.Second, Available: true, Run: func(ctx context.Context, _ json.RawMessage) error {
			return telemetry.RetentionPass(ctx, telemetryStore, retention, logger)
		}},
		{ID: "alert-evaluation", Name: "Evaluate alerts", Description: "Evaluate configured alert rules against current observations.", Interval: 30 * time.Second, Timeout: 25 * time.Second, Available: true, Run: func(ctx context.Context, _ json.RawMessage) error { return alerts.Evaluate(ctx, database.Pool) }},
		{ID: "backup-scheduling", Name: "Schedule due backups", Description: "Queue due backup policies; individual policy schedules remain in backup settings.", Interval: 30 * time.Second, Timeout: 25 * time.Second, Available: true, Run: func(ctx context.Context, _ json.RawMessage) error {
			_, err := backups.ScheduleOnce(ctx, database.Pool, time.Now().UTC())
			return err
		}},
		{ID: "oauth-cleanup", Name: "OAuth expiration cleanup", Description: "Expire old MCP authorization codes and grants.", Interval: time.Hour, Timeout: 10 * time.Second, Available: mcpHandler != nil, Run: func(ctx context.Context, _ json.RawMessage) error { return mcpHandler.Cleanup(ctx) }},
	})
	if err := tasks.Start(ctx); err != nil {
		logger.Error("scheduled task initialization failed", "error", err)
		os.Exit(1)
	}
	defer tasks.Close()
	tasksMux := http.NewServeMux()
	tasks.Register(tasksMux, authService, auditRecorder)
	router.Handle("/api/admin/*", tasksMux)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("server shutdown failed", "error", err)
		}
	}()
	go stackAPI.Run(ctx)
	go stackAPI.RunImageUpdates(ctx)
	go notifications.Run(ctx, database.Pool, destinations, logger)
	logger.Info("starting Vigara API", "addr", cfg.Address)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
}

// splitList parses a comma-separated environment value, ignoring blanks.
func splitList(value string) []string {
	var items []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}
