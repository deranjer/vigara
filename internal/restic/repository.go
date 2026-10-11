package restic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const maxSnapshots = 10000

var fullSnapshotIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Snapshot contains only the metadata needed to identify and browse a snapshot.
// Repository credentials and command output are never returned to callers.
type Snapshot struct {
	ID      string    `json:"id"`
	Time    time.Time `json:"time"`
	Host    string    `json:"hostname,omitempty"`
	Paths   []string  `json:"paths"`
	Tags    []string  `json:"tags,omitempty"`
	Version string    `json:"programVersion,omitempty"`
}

// SnapshotEntry is one item in a snapshot tree. Names are bounded and are
// supplied by the repository, so callers should display them as data.
type SnapshotEntry struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Path string `json:"path"`
	Size uint64 `json:"size,omitempty"`
}

// RetentionPlan contains only the identifiers and timestamps an operator needs
// to review the dry-run result; Restic's host, user, paths, and raw output are dropped.
type RetentionPlan struct {
	Groups []RetentionGroup `json:"groups"`
}

type RetentionGroup struct {
	Keep   []RetentionSnapshot `json:"keep"`
	Remove []RetentionSnapshot `json:"remove"`
}

type RetentionSnapshot struct {
	ID   string    `json:"id"`
	Time time.Time `json:"time"`
}

// ListSnapshots returns a bounded, sanitized list of repository snapshots.
func (r *Runner) ListSnapshots(ctx context.Context, repository string) ([]Snapshot, error) {
	if err := r.validateForRead(ctx, repository); err != nil {
		return nil, err
	}
	data, _, err := r.invoke(ctx, []string{"--repo", repository, "snapshots", "--json"})
	if err != nil {
		return nil, safeCommandError("list snapshots", err)
	}
	var raw []struct {
		ID      string    `json:"id"`
		Time    time.Time `json:"time"`
		Host    string    `json:"hostname"`
		Paths   []string  `json:"paths"`
		Tags    []string  `json:"tags"`
		Version string    `json:"program_version"`
	}
	if err := json.Unmarshal(data, &raw); err != nil || len(raw) > maxSnapshots {
		return nil, errors.New("restic: invalid or excessive snapshot metadata")
	}
	result := make([]Snapshot, 0, len(raw))
	for _, s := range raw {
		if !fullSnapshotIDPattern.MatchString(s.ID) || s.Time.IsZero() || len(s.Paths) > 256 || len(s.Tags) > 256 {
			return nil, errors.New("restic: invalid snapshot metadata")
		}
		result = append(result, Snapshot{ID: s.ID, Time: s.Time, Host: safeText(s.Host, 256), Paths: safeStrings(s.Paths), Tags: safeStrings(s.Tags), Version: safeText(s.Version, 64)})
	}
	return result, nil
}

// BrowseSnapshot lists the filesystem entries in one explicitly identified snapshot.
func (r *Runner) BrowseSnapshot(ctx context.Context, repository, snapshotID string) ([]SnapshotEntry, error) {
	if !fullSnapshotIDPattern.MatchString(snapshotID) {
		return nil, errors.New("restic: full snapshot ID is required")
	}
	if err := r.validateForRead(ctx, repository); err != nil {
		return nil, err
	}
	data, _, err := r.invoke(ctx, []string{"--repo", repository, "ls", "--json", snapshotID})
	if err != nil {
		return nil, safeCommandError("browse snapshot", err)
	}
	entries, err := parseEntries(data)
	if err != nil {
		return nil, errors.New("restic: invalid or excessive snapshot listing")
	}
	return entries, nil
}

// CheckRepository performs Restic's read-only repository integrity check.
func (r *Runner) CheckRepository(ctx context.Context, repository string) error {
	if err := r.validateForRead(ctx, repository); err != nil {
		return err
	}
	data, _, err := r.invoke(ctx, []string{"--repo", repository, "check", "--json"})
	if err != nil {
		return safeCommandError("check repository", err)
	}
	var summary struct {
		Type      string `json:"message_type"`
		NumErrors int    `json:"num_errors"`
	}
	found := false
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var candidate struct {
			Type      string `json:"message_type"`
			NumErrors int    `json:"num_errors"`
		}
		if json.Unmarshal(line, &candidate) != nil {
			return errors.New("restic: invalid repository check output")
		}
		if candidate.Type == "summary" {
			summary, found = candidate, true
		}
	}
	if !found || summary.NumErrors != 0 {
		return errors.New("restic: repository check reported errors")
	}
	return nil
}

// RestoreSnapshot restores the selected snapshot into a caller-validated target.
// The command never enables overwrite and accepts only a complete snapshot ID.
func (r *Runner) RestoreSnapshot(ctx context.Context, repository, snapshotID, target string) error {
	if !fullSnapshotIDPattern.MatchString(snapshotID) {
		return errors.New("restic: full snapshot ID is required")
	}
	if strings.TrimSpace(target) == "" {
		return errors.New("restic: restore target is required")
	}
	if err := r.validateForRead(ctx, repository); err != nil {
		return err
	}
	_, _, err := r.invoke(ctx, []string{"--repo", repository, "restore", "--target", target, snapshotID})
	if err != nil {
		return safeCommandError("restore snapshot", err)
	}
	return nil
}

// PlanRetention runs forget with --dry-run. It never removes snapshots.
func (r *Runner) PlanRetention(ctx context.Context, repository string, keepLast int) (RetentionPlan, error) {
	if keepLast < 1 || keepLast > maxSnapshots {
		return RetentionPlan{}, errors.New("restic: keep-last must be between 1 and 10000")
	}
	if err := r.validateForRead(ctx, repository); err != nil {
		return RetentionPlan{}, err
	}
	data, _, err := r.invoke(ctx, []string{"--repo", repository, "forget", "--dry-run", "--json", "--keep-last", fmt.Sprint(keepLast)})
	if err != nil {
		return RetentionPlan{}, safeCommandError("plan retention", err)
	}
	if !json.Valid(data) || len(data) > maxOutputBytes {
		return RetentionPlan{}, errors.New("restic: invalid retention plan")
	}
	var raw []struct {
		Keep []struct {
			ID   string    `json:"id"`
			Time time.Time `json:"time"`
		} `json:"keep"`
		Remove []struct {
			ID   string    `json:"id"`
			Time time.Time `json:"time"`
		} `json:"remove"`
	}
	if err := json.Unmarshal(data, &raw); err != nil || len(raw) > maxSnapshots {
		return RetentionPlan{}, errors.New("restic: invalid retention plan")
	}
	plan := RetentionPlan{Groups: make([]RetentionGroup, 0, len(raw))}
	count := 0
	for _, group := range raw {
		clean := RetentionGroup{Keep: make([]RetentionSnapshot, 0, len(group.Keep)), Remove: make([]RetentionSnapshot, 0, len(group.Remove))}
		for _, snapshot := range group.Keep {
			if !snapshotIDPattern.MatchString(snapshot.ID) {
				return RetentionPlan{}, errors.New("restic: invalid retention plan")
			}
			clean.Keep = append(clean.Keep, RetentionSnapshot{ID: snapshot.ID, Time: snapshot.Time})
			count++
		}
		for _, snapshot := range group.Remove {
			if !snapshotIDPattern.MatchString(snapshot.ID) {
				return RetentionPlan{}, errors.New("restic: invalid retention plan")
			}
			clean.Remove = append(clean.Remove, RetentionSnapshot{ID: snapshot.ID, Time: snapshot.Time})
			count++
		}
		if count > maxSnapshots {
			return RetentionPlan{}, errors.New("restic: excessive retention plan")
		}
		plan.Groups = append(plan.Groups, clean)
	}
	return plan, nil
}

func (r *Runner) validateForRead(ctx context.Context, repository string) error {
	if strings.TrimSpace(repository) == "" {
		return errors.New("restic: repository is required")
	}
	if _, err := r.version(ctx); err != nil {
		return safeCommandError("validate restic version", err)
	}
	return nil
}

func safeText(value string, max int) string {
	if len(value) > max {
		return value[:max]
	}
	return value
}

func safeStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, safeText(value, 4096))
	}
	return out
}

func parseEntries(data []byte) ([]SnapshotEntry, error) {
	var result []SnapshotEntry
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var raw struct {
			Type     string `json:"type"`
			Name     string `json:"name"`
			Path     string `json:"path"`
			Size     uint64 `json:"size"`
			NodeType string `json:"struct_type"`
		}
		if err := json.Unmarshal(line, &raw); err != nil {
			return nil, err
		}
		if raw.Name == "" && raw.Path == "" {
			continue
		}
		if len(raw.Name) > 4096 || len(raw.Path) > 4096 || len(result) >= 100000 {
			return nil, errors.New("entry limit")
		}
		kind := raw.Type
		if kind == "" {
			kind = raw.NodeType
		}
		result = append(result, SnapshotEntry{Name: raw.Name, Type: kind, Path: raw.Path, Size: raw.Size})
	}
	return result, nil
}
