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
	agentchannel "github.com/deranjer/vigara/apps/agent/internal/channel"
	"github.com/deranjer/vigara/apps/agent/internal/dockerapi"
	"github.com/deranjer/vigara/apps/agent/internal/dockerchannel"
	agentruntime "github.com/deranjer/vigara/apps/agent/internal/runtime"
	"github.com/deranjer/vigara/internal/composeadoption"
	"github.com/deranjer/vigara/internal/protocol"
	"github.com/deranjer/vigara/internal/serverurl"
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
		server := flags.String("server", "", "Vigira server URL (HTTPS, or loopback HTTP)")
		name := flags.String("name", hostname(), "device display name")
		insecure := flags.Bool("allow-insecure-private-http", false, "allow plain HTTP to a private-network or Tailscale server address (credentials and telemetry are unencrypted)")
		if err := flags.Parse(rest[1:]); err != nil {
			return err
		}
		if *server == "" {
			return fmt.Errorf("enroll requires --server")
		}
		client.State.AllowInsecurePrivateHTTP = *insecure
		if err := serverurl.Validate(*server, *insecure); err != nil {
			return err
		}
		if *insecure && !serverurl.IsSecure(*server) {
			logger.Warn("server connection is not encrypted; use only on a trusted private network or VPN", "server", *server)
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
		live := agentchannel.NewClient(client.State.ServerURL, client.State.Credential, agentruntime.AgentVersion, client.State.AllowInsecurePrivateHTTP, client.Capabilities, logger)
		registerDocker(live, logger)
		go live.Run(ctx)
		return client.Run(ctx)
	})
}

// registerDocker offers the Docker read methods over the live channel, but only when
// the local operator enabled Docker collection with a valid endpoint.
func registerDocker(live *agentchannel.Client, logger *slog.Logger) {
	if !strings.EqualFold(strings.TrimSpace(os.Getenv("VIGIRA_DOCKER_ENABLED")), "true") {
		return
	}
	api, err := dockerapi.New(strings.TrimSpace(os.Getenv("VIGIRA_DOCKER_HOST")), nil)
	if err != nil {
		logger.Warn("Docker live views are unavailable: the Docker endpoint configuration is invalid")
		return
	}
	dockerchannel.Register(live, api)
	dockerchannel.RegisterResources(live, api)
	if protocol.DockerTierRank(protocol.DockerTier(live.Capabilities())) >= protocol.DockerTierRank(protocol.DockerModeManage) {
		dockerchannel.RegisterManage(live, api)
		dockerchannel.RegisterResourceManagement(live, api)
		logger.Info("container management is enabled on this host", "tier", protocol.DockerTier(live.Capabilities()))
	}
	if protocol.DockerTierRank(protocol.DockerTier(live.Capabilities())) >= protocol.DockerTierRank(protocol.DockerModeExec) {
		dockerchannel.RegisterExec(live, api)
		logger.Info("container terminals are enabled on this host")
	}
}

func interruptContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
