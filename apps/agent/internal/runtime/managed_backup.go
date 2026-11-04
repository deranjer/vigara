package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/deranjer/vigara/internal/protocol"
	"github.com/deranjer/vigara/internal/restic"
)

// ConfigureManagedBackups is an installer-owned opt-in, not a server setting.
func ConfigureManagedBackups(statePath string) error {
	state, err := Load(statePath)
	if err != nil {
		return err
	}
	if _, exists := state.ResticProfiles["managed"]; exists {
		return errors.New("local profile ID managed is reserved; rename it before enabling management")
	}
	state.ManagedBackups = true
	return Save(statePath, state)
}

func bundledRestic() string {
	exe, err := os.Executable()
	if err != nil {
		return "restic"
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(filepath.Dir(exe), "restic.exe")
	}
	return filepath.Join(filepath.Dir(exe), "vigara-restic")
}

func prospectivePath(value string) string {
	var tail []string
	for {
		if resolved, err := filepath.EvalSymlinks(value); err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return resolved
		}
		parent := filepath.Dir(value)
		if parent == value {
			return value
		}
		tail = append(tail, filepath.Base(value))
		value = parent
	}
}

func pathContains(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && (rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func (c *Client) backupProfiles() map[string]ResticProfile {
	profiles := make(map[string]ResticProfile, len(c.State.ResticProfiles)+1)
	for id, p := range c.State.ResticProfiles {
		profiles[id] = p
	}
	if c.State.ManagedBackups && c.State.ManagedBackupProfile != nil {
		profiles["managed"] = *c.State.ManagedBackupProfile
	}
	return profiles
}

func (c *Client) applyManagedBackup(ctx context.Context, desired *protocol.ManagedBackup) {
	if !c.State.ManagedBackups || desired == nil {
		return
	}
	if c.State.ManagedBackupStatus != nil && c.State.ManagedBackupStatus.Revision == desired.Revision && c.State.ManagedBackupStatus.ErrorCode == "" {
		return
	}
	status := &protocol.ManagedBackupStatus{Revision: desired.Revision}
	c.State.ManagedBackupStatus = status
	p := ResticProfile{Enabled: true, Repository: desired.Repository, SourcePaths: desired.SourcePaths, Executable: bundledRestic(), Environment: map[string]string{"RESTIC_PASSWORD": desired.Password}}
	if desired.Revision < 1 || desired.Password == "" || !filepath.IsAbs(p.Repository) || !validResticProfile(p) {
		status.ErrorCode = "invalid_configuration"
		return
	}
	for _, source := range p.SourcePaths {
		if !filepath.IsAbs(source) {
			status.ErrorCode = "invalid_configuration"
			return
		}
		f, err := os.Open(source)
		if err != nil {
			status.ErrorCode = "source_unavailable"
			return
		}
		_ = f.Close()
		// Do not recursively back up the repository into itself.
		src, e1 := filepath.EvalSymlinks(source)
		repo := prospectivePath(p.Repository)
		if e1 == nil {
			if pathContains(src, repo) || pathContains(repo, src) {
				status.ErrorCode = "invalid_configuration"
				return
			}
		}
	}
	if _, err := os.Stat(p.Executable); err != nil {
		status.ErrorCode = "restic_unavailable"
		return
	}
	checkCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	runner := restic.New(restic.Config{Executable: p.Executable, Environment: p.Environment})
	if desired.Initialize {
		if err := runner.InitializeMissing(checkCtx, p.Repository); err != nil {
			status.ErrorCode = "repository_unavailable"
			return
		}
	}
	if _, err := runner.ValidateRepositoryVersion(checkCtx, p.Repository); err != nil {
		status.ErrorCode = "repository_unavailable"
		return
	}
	c.State.ManagedBackupProfile = &p
}
