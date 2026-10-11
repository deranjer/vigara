// vigira-restic provides local operator recovery and repository inspection.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/deranjer/vigara/apps/agent/internal/backupprep"
	agentruntime "github.com/deranjer/vigara/apps/agent/internal/runtime"
	"github.com/deranjer/vigara/internal/restic"
)

const maxCLIOutput = 16 << 20

var repositoryScheme = regexp.MustCompile(`^[a-z][a-z0-9+.-]*:`)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "vigira-restic: operation failed; details are withheld to protect local paths and credentials")
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("command required")
	}
	config, rest := extractConfig(args)
	if config == "" {
		return errors.New("agent config unavailable")
	}
	state, err := agentruntime.Load(config)
	if err != nil {
		return errors.New("invalid agent config")
	}
	if len(rest) == 0 {
		return errors.New("command required")
	}
	command := rest[0]
	flags := flag.NewFlagSet("vigira-restic "+command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	profileID := flags.String("profile", "", "enabled local profile ID")
	snapshotID := flags.String("snapshot", "", "full snapshot ID")
	target := flags.String("target", "", "new empty restore destination")
	keepLast := flags.Int("keep-last", 0, "snapshots to keep in dry-run plan")
	if err := flags.Parse(rest[1:]); err != nil {
		return errors.New("invalid arguments")
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	if !validProfileID(*profileID) {
		return errors.New("invalid profile")
	}
	profile, ok := state.ResticProfiles[*profileID]
	if !ok || !profile.Enabled || !validProfile(profile) {
		return errors.New("profile unavailable")
	}
	runner := restic.New(restic.Config{Executable: profile.Executable, Environment: profile.Environment})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var result any
	switch command {
	case "snapshots":
		if *snapshotID != "" || *target != "" || *keepLast != 0 {
			return errors.New("unexpected option")
		}
		result, err = runner.ListSnapshots(ctx, profile.Repository)
	case "browse":
		if !fullID(*snapshotID) || *target != "" || *keepLast != 0 {
			return errors.New("full snapshot ID required")
		}
		result, err = runner.BrowseSnapshot(ctx, profile.Repository, *snapshotID)
	case "check":
		if *snapshotID != "" || *target != "" || *keepLast != 0 {
			return errors.New("unexpected option")
		}
		err = runner.CheckRepository(ctx, profile.Repository)
		result = map[string]string{"status": "passed"}
	case "restore":
		if !fullID(*snapshotID) || *target == "" || *keepLast != 0 {
			return errors.New("restore requires snapshot and target")
		}
		canonical, targetErr := validateTarget(*target, profile)
		if targetErr != nil {
			return errors.New("unsafe restore target")
		}
		err = runner.RestoreSnapshot(ctx, profile.Repository, *snapshotID, canonical)
		result = map[string]string{"status": "restored", "snapshotId": *snapshotID, "target": canonical}
	case "retention-plan":
		if *snapshotID != "" || *target != "" {
			return errors.New("unexpected option")
		}
		if *keepLast < 1 || *keepLast > 10000 {
			return errors.New("invalid keep-last")
		}
		result, err = runner.PlanRetention(ctx, profile.Repository, *keepLast)
	default:
		return errors.New("unknown command")
	}
	if err != nil {
		return err
	}
	data, err := json.Marshal(result)
	if err != nil || len(data) > maxCLIOutput {
		return errors.New("result exceeded limit")
	}
	_, err = out.Write(append(data, '\n'))
	return err
}

func extractConfig(args []string) (string, []string) {
	path := os.Getenv("VIGIRA_AGENT_CONFIG")
	if path == "" {
		if dir, err := os.UserConfigDir(); err == nil {
			path = filepath.Join(dir, "vigira", "agent.json")
		}
	}
	remaining := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--config" && i+1 < len(args) {
			path = args[i+1]
			i++
			continue
		}
		if strings.HasPrefix(args[i], "--config=") {
			path = strings.TrimPrefix(args[i], "--config=")
			continue
		}
		remaining = append(remaining, args[i])
	}
	return path, remaining
}

func validProfileID(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func fullID(value string) bool {
	return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == ""
}

func validProfile(p agentruntime.ResticProfile) bool {
	if !validLocalRepository(p.Repository) || len(p.SourcePaths) > 256 || len(p.SourcePaths) == 0 && (p.PostgreSQL == nil || !p.PostgreSQL.Enabled) || len(p.Environment) > 64 {
		return false
	}
	if p.PostgreSQL != nil && p.PostgreSQL.Enabled && backupprep.ValidatePostgreSQLConfig(backupprep.PostgreSQLConfig{Environment: p.PostgreSQL.Environment}) != nil {
		return false
	}
	for _, path := range p.SourcePaths {
		if path == "" || len(path) > 4096 || strings.ContainsRune(path, 0) {
			return false
		}
	}
	for key, value := range p.Environment {
		upper := strings.ToUpper(key)
		if len(key) > 128 || len(value) > 8192 || strings.ContainsAny(key+value, "\x00\r\n") || strings.HasSuffix(upper, "_PASSWORD_COMMAND") || strings.HasSuffix(upper, "_REPOSITORY_FILE") {
			return false
		}
	}
	return len(p.Executable) <= 4096 && !strings.ContainsAny(p.Executable, "\x00\r\n")
}

func validLocalRepository(repository string) bool {
	repository = strings.TrimSpace(repository)
	if repository == "" || len(repository) > 4096 || strings.ContainsAny(repository, "\x00\r\n") {
		return false
	}
	if strings.HasPrefix(repository, "local:") {
		return true
	}
	if len(repository) >= 3 && repository[1] == ':' && (repository[2] == '\\' || repository[2] == '/') {
		return true
	}
	return !repositoryScheme.MatchString(repository)
}

func validateTarget(target string, profile agentruntime.ResticProfile) (string, error) {
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	canonical := filepath.Clean(abs)
	info, err := os.Lstat(canonical)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("target must be an existing real directory")
	}
	items, err := os.ReadDir(canonical)
	if err != nil || len(items) != 0 {
		return "", errors.New("target must be empty")
	}
	for _, path := range rangeAncestors(canonical) {
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("target path contains a symlink")
		}
	}
	for _, source := range append([]string{localRepositoryPath(profile.Repository)}, profile.SourcePaths...) {
		if overlaps(canonical, source) {
			return "", errors.New("target overlaps repository or backup source")
		}
	}
	return canonical, nil
}

func rangeAncestors(path string) []string {
	var result []string
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		result = append(result, current)
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	return result
}

func overlaps(a, b string) bool {
	bAbs, err := filepath.Abs(b)
	if err != nil {
		return true
	}
	bAbs, err = resolveExistingPrefix(bAbs)
	if err != nil {
		return true
	}
	a, bAbs = filepath.Clean(a), filepath.Clean(bAbs)
	if filepath.Separator == '\\' {
		a, bAbs = strings.ToLower(a), strings.ToLower(bAbs)
	}
	return within(a, bAbs) || within(bAbs, a)
}

func resolveExistingPrefix(path string) (string, error) {
	current := filepath.Clean(path)
	var suffix []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved), nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func localRepositoryPath(repository string) string {
	if strings.HasPrefix(repository, "local:") {
		return strings.TrimPrefix(repository, "local:")
	}
	return repository
}

func within(path, base string) bool {
	rel, err := filepath.Rel(base, path)
	return err == nil && (rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
