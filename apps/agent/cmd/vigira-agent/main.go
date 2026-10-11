package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	dockercollector "github.com/deranjer/vigara/apps/agent/internal/collector/docker"
	agentruntime "github.com/deranjer/vigara/apps/agent/internal/runtime"
	"github.com/deranjer/vigara/internal/composeadoption"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := execute(os.Args[1:], logger); err != nil {
		logger.Error("agent stopped", "error", err)
		os.Exit(1)
	}
}

func execute(args []string, logger *slog.Logger) error {
	if len(args) == 0 {
		args = []string{"run"}
	}
	statePath, rest := extractStatePath(args)
	if len(rest) == 0 {
		return fmt.Errorf("usage: vigira-agent [--config PATH] enroll --server URL | run | compose-discover --root PATH")
	}
	switch rest[0] {
	case "enroll":
		client, err := agentruntime.NewClient(statePath)
		if err != nil {
			return err
		}
		flags := flag.NewFlagSet("enroll", flag.ContinueOnError)
		server := flags.String("server", "", "Vigira server HTTPS URL")
		name := flags.String("name", hostname(), "device display name")
		if err := flags.Parse(rest[1:]); err != nil {
			return err
		}
		if *server == "" {
			return fmt.Errorf("enroll requires --server")
		}
		logger.Info("enter one-time enrollment token")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return fmt.Errorf("read enrollment token: %w", err)
		}
		token := strings.TrimSpace(line)
		if token == "" {
			return fmt.Errorf("enrollment token is empty")
		}
		if err := client.Enroll(context.Background(), *server, token, *name); err != nil {
			return err
		}
		logger.Info("agent enrolled", "agentId", client.State.AgentID, "server", client.State.ServerURL, "stateFile", statePath)
		return nil
	case "run":
		client, err := agentruntime.NewClient(statePath)
		if err != nil {
			return err
		}
		if client.State.AgentID == "" {
			return fmt.Errorf("agent is not enrolled; run enroll first")
		}
		return runAgent(client, logger)
	case "compose-discover":
		return discoverCompose(rest[1:])
	default:
		return fmt.Errorf("usage: vigira-agent [--config PATH] enroll --server URL | run | compose-discover --root PATH")
	}
}

// discoverCompose is an operator-invoked, read-only local discovery command.
// Its JSON contains only paths, project labels, runtime state, IDs/start times,
// and mount bindings; no env, Config.Env, raw labels, or Compose values.
func discoverCompose(args []string) error {
	flags := flag.NewFlagSet("compose-discover", flag.ContinueOnError)
	root := flags.String("root", "", "local directory containing Compose applications")
	if err := flags.Parse(args); err != nil {
		return errors.New("invalid compose-discover options")
	}
	if *root == "" {
		return errors.New("compose-discover requires --root")
	}
	host := strings.TrimSpace(os.Getenv("VIGIRA_DOCKER_HOST"))
	if host == "" {
		return errors.New("compose-discover requires locally configured VIGIRA_DOCKER_HOST")
	}
	collector, err := dockercollector.New(dockercollector.Config{Enabled: true, Host: host})
	if err != nil {
		return errors.New("local Docker endpoint configuration is invalid")
	}
	snapshot, err := collector.ObserveCompose(context.Background())
	if err != nil {
		return err
	}
	preview, err := composeadoption.Discover(*root, snapshot)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(preview); err != nil {
		return errors.New("could not write adoption preview")
	}
	return nil
}

func extractStatePath(args []string) (string, []string) {
	path := os.Getenv("VIGIRA_AGENT_CONFIG")
	if path == "" {
		if dir, err := os.UserConfigDir(); err == nil {
			path = filepath.Join(dir, "vigira", "agent.json")
		}
	}
	if path == "" {
		path = filepath.Join(".", "vigira-agent.json")
	}
	for i := 0; i < len(args); i++ {
		if args[i] == "--config" && i+1 < len(args) {
			path = args[i+1]
			args = append(args[:i], args[i+2:]...)
			break
		}
	}
	return path, args
}

func hostname() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "Vigira agent"
	}
	return name
}

func runAgent(client *agentruntime.Client, logger *slog.Logger) error {
	return runAsService(func(ctx context.Context) error {
		logger.Info("agent service started", "agentId", client.State.AgentID)
		return client.Run(ctx)
	})
}

func interruptContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
