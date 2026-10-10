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
	"github.com/go-chi/chi/v5"
)

type healthOutput struct {
	Body struct {
		Status string `json:"status" example:"ok"`
	}
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	router := chi.NewRouter()
	api := humachi.New(router, huma.DefaultConfig("Vigira API", "0.1.0"))
	huma.Register(api, huma.Operation{
		OperationID: "healthCheck", Method: http.MethodGet, Path: "/healthz", Summary: "Check service health",
	}, func(context.Context, *struct{}) (*healthOutput, error) {
		out := &healthOutput{}
		out.Body.Status = "ok"
		return out, nil
	})
	staticFS, err := fs.Sub(webassets.FS, "web")
	if err != nil {
		logger.Error("web assets unavailable", "error", err)
		os.Exit(1)
	}
	router.Handle("/*", http.FileServer(http.FS(staticFS)))
	addr := os.Getenv("VIGIRA_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	server := &http.Server{Addr: addr, Handler: router, ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("server shutdown failed", "error", err)
		}
	}()
	logger.Info("starting Vigira API", "addr", addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
}
