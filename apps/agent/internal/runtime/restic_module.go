package runtime

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode"

	"github.com/deranjer/vigara/internal/modules"
	"github.com/deranjer/vigara/internal/restic"
)

const resticModuleID = "restic.backup"
const resticModuleVersion = "1.0.0"
const resticCapability = "restic.backup"

var repositoryScheme = regexp.MustCompile(`^[a-z][a-z0-9+.-]*:`)

type resticBackupModule struct {
	profiles map[string]ResticProfile
}

func (m resticBackupModule) Descriptor() modules.Descriptor {
	return modules.Descriptor{
		ID: resticModuleID, Version: resticModuleVersion,
		Platforms:     []string{"linux", "windows", "darwin"},
		Capabilities:  []string{resticCapability},
		Configuration: []modules.ConfigField{{Name: "profileId", Type: "string", Required: true}},
		Permissions:   []modules.PermissionSpec{{Name: resticCapability, Description: "Run a backup using the named local profile"}},
	}
}

func (m resticBackupModule) Execute(ctx context.Context, job modules.JobContext) (modules.Result, error) {
	return m.ExecuteWithProgress(ctx, job, nil)
}

func (m resticBackupModule) ExecuteWithProgress(ctx context.Context, job modules.JobContext, onProgress func(restic.Progress) error) (modules.Result, error) {
	if len(job.Configuration) != 1 {
		return modules.Result{}, errors.New("invalid_profile")
	}
	profileID, ok := job.Configuration["profileId"].(string)
	if !ok || !validProfileID(profileID) {
		return modules.Result{}, errors.New("invalid_profile")
	}
	profile, ok := m.profiles[profileID]
	if !ok || !profile.Enabled || !validResticProfile(profile) {
		return modules.Result{}, errors.New("profile_unavailable")
	}
	if !hasGrant(job.Permissions, resticCapability, "profile:"+profileID) {
		return modules.Result{}, errors.New("permission_denied")
	}
	if onProgress != nil {
		if err := onProgress(restic.Progress{Phase: "validating"}); err != nil {
			return modules.Result{}, errors.New("progress_unavailable")
		}
	}
	runner := restic.New(restic.Config{Executable: profile.Executable, Environment: profile.Environment})
	version, err := runner.ValidateRepositoryVersion(ctx, profile.Repository)
	if err != nil {
		status, code := restic.Failed, "repository_validation_failed"
		if errors.Is(ctx.Err(), context.Canceled) {
			status, code = restic.Canceled, "canceled"
		} else if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			status, code = restic.Interrupted, "deadline_exceeded"
		}
		return modules.Result{Output: map[string]any{"resticStatus": status, "resticVersion": version, "errorCode": code}}, nil
	}
	backup := runner.BackupWithProgress(ctx, profile.Repository, profile.SourcePaths, onProgress)
	if backup.ResticVersion == "" {
		backup.ResticVersion = version
	}
	output := map[string]any{
		"resticStatus":  backup.Status,
		"resticVersion": backup.ResticVersion,
		"snapshotIds":   backup.SnapshotIDs,
	}
	if backup.ErrorCode != "" {
		output["errorCode"] = backup.ErrorCode
	}
	return modules.Result{Output: output}, nil
}

func validProfileID(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func validResticProfile(profile ResticProfile) bool {
	if !validLocalRepository(profile.Repository) || len(profile.SourcePaths) == 0 || len(profile.SourcePaths) > 256 {
		return false
	}
	for _, path := range profile.SourcePaths {
		if strings.TrimSpace(path) == "" || len(path) > 4096 || strings.ContainsRune(path, '\x00') {
			return false
		}
	}
	if len(profile.Environment) > 64 {
		return false
	}
	for key, value := range profile.Environment {
		if key == "" || len(key) > 128 || len(value) > 8192 || strings.ContainsRune(value, '\x00') {
			return false
		}
		for _, r := range key {
			if unicode.IsControl(r) || r == '=' {
				return false
			}
		}
		if strings.EqualFold(key, "RESTIC_PASSWORD_COMMAND") || strings.EqualFold(key, "RESTIC_FROM_PASSWORD_COMMAND") || strings.EqualFold(key, "RESTIC_REPOSITORY_FILE") || strings.EqualFold(key, "RESTIC_FROM_REPOSITORY_FILE") {
			return false
		}
	}
	return len(profile.Executable) <= 4096
}

func validLocalRepository(repository string) bool {
	repository = strings.TrimSpace(repository)
	if repository == "" || len(repository) > 4096 || strings.ContainsRune(repository, '\x00') {
		return false
	}
	if strings.HasPrefix(repository, "local:") {
		return true
	}
	// Permit Windows drive paths while rejecting URL/backend schemes such as
	// S3, REST, SFTP, and cloud storage targets from the initial feature scope.
	if len(repository) >= 3 && repository[1] == ':' && (repository[2] == '\\' || repository[2] == '/') {
		return true
	}
	return !repositoryScheme.MatchString(repository)
}

func hasGrant(grants []modules.PermissionGrant, name, scope string) bool {
	for _, grant := range grants {
		if grant.Name == name && grant.Scope == scope {
			return true
		}
	}
	return false
}

func resticCapabilityEnabled(profiles map[string]ResticProfile) bool {
	for id, profile := range profiles {
		if validProfileID(id) && profile.Enabled && validResticProfile(profile) {
			return true
		}
	}
	return false
}
