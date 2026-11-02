package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/agent"
	"github.com/deranjer/vigara/apps/api/internal/config"
	"github.com/deranjer/vigara/apps/api/internal/database"
)

func hostAgentBundle(args []string, output, diagnostics io.Writer) error {
	flags := flag.NewFlagSet("host-agent-bundle", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	var options agent.HostAgentOptions
	flags.StringVar(&options.Name, "name", "", "host device name")
	flags.StringVar(&options.Target, "target", "", "Linux host platform")
	flags.StringVar(&options.ServerURL, "server", "", "server origin reachable from the host")
	flags.StringVar(&options.DockerHost, "docker-host", "", "local Docker Unix socket")
	flags.StringVar(&options.ManagedRoot, "compose-root", "", "shared Compose directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || options.Name == "" || options.Target == "" || options.ServerURL == "" || options.DockerHost == "" || options.ManagedRoot == "" {
		return fmt.Errorf("host-agent-bundle requires --name, --target, --server, --docker-host and --compose-root")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	db, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connect to running server database: %w", err)
	}
	defer db.Close()
	handler := agent.NewHandler(db.Pool, nil)
	handler.ConfigureDownloads("", os.Getenv("VIGIRA_AGENT_RELEASE_DIR"))
	if err := handler.ConfigureReleaseSync(os.Getenv("VIGIRA_AGENT_RELEASE_CACHE"), os.Getenv("VIGIRA_AGENT_RELEASE_TAG"), revision); err != nil {
		return err
	}
	return handler.WriteHostAgentBundle(ctx, output, options)
}
