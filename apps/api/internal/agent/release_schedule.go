package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

var errIncompatibleRelease = errors.New("agent release uses a different protocol or Restic version")

type ReleaseRetentionSettings struct {
	PurgeOld     bool `json:"purgeOld"`
	KeepVersions int  `json:"keepVersions"`
}

func ParseReleaseRetentionSettings(raw json.RawMessage) (ReleaseRetentionSettings, error) {
	var input struct {
		PurgeOld     *bool `json:"purgeOld"`
		KeepVersions *int  `json:"keepVersions"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || input.PurgeOld == nil || input.KeepVersions == nil || *input.KeepVersions < 1 || *input.KeepVersions > 20 {
		return ReleaseRetentionSettings{}, errors.New("bundle retention requires purgeOld and keepVersions between 1 and 20")
	}
	return ReleaseRetentionSettings{PurgeOld: *input.PurgeOld, KeepVersions: *input.KeepVersions}, nil
}

type publishedAgentRelease struct {
	Tag       string    `json:"tag_name"`
	Draft     bool      `json:"draft"`
	Published time.Time `json:"published_at"`
}

func (h *Handler) ReleaseSyncEnabled() bool { return h.releaseStore != nil }

// RefreshReleases is the scheduled maintenance operation. Automatic mode serves
// the newest complete release sharing this server's protocol and pinned Restic.
func (h *Handler) RefreshReleases(ctx context.Context, purge bool, keep int) (err error) {
	s := h.releaseStore
	if s == nil {
		return errors.New("automatic agent release downloads are not configured")
	}
	if keep < 1 || keep > 20 {
		return errors.New("keepVersions must be between 1 and 20")
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	defer func() {
		if err != nil {
			s.mu.Lock()
			s.status.State, s.status.Error = "error", err.Error()
			s.mu.Unlock()
		}
	}()
	child := func(tag string) *releaseStore {
		return &releaseStore{root: filepath.Join(s.cacheRoot, tag), client: s.client, status: releaseSyncStatus{Enabled: true, Tag: tag, State: "pending"}}
	}
	activate := func(candidate *releaseStore) error {
		h.releaseFilesMu.Lock()
		defer h.releaseFilesMu.Unlock()
		// A restart can reuse the selected release even if its server-commit
		// fallback has already aged out of the retained versions.
		file, e := os.CreateTemp(s.cacheRoot, ".active-")
		if e != nil {
			return errors.New("could not persist active agent release")
		}
		defer os.Remove(file.Name())
		_, writeErr := file.WriteString(candidate.status.Tag + "\n")
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil || os.Rename(file.Name(), filepath.Join(s.cacheRoot, "active-tag")) != nil {
			return errors.New("could not persist active agent release")
		}
		s.mu.Lock()
		s.dir, s.root, s.status = candidate.dir, candidate.root, candidate.status
		s.mu.Unlock()
		return nil
	}
	if !s.automatic {
		if err = s.sync(ctx); err != nil {
			return err
		}
		if err = activate(s); err != nil {
			return err
		}
		if purge {
			return h.purgeReleaseCache(map[string]bool{h.releaseStatus().Tag: true}, keep)
		}
		return nil
	}
	if h.artifactDirectory() == "" {
		if b, e := readBoundedFile(filepath.Join(s.cacheRoot, "active-tag"), 128); e == nil {
			tag := strings.TrimSpace(string(b))
			if releaseTagPattern.MatchString(tag) {
				candidate := child(tag)
				if directory := cachedRelease(candidate.root, tag); directory != "" {
					candidate.ready(directory)
					_ = activate(candidate)
				}
			}
		}
		if h.artifactDirectory() == "" {
			_ = s.sync(ctx)
		}
	}
	s.mu.Lock()
	s.status.State, s.status.Error = "downloading", ""
	s.mu.Unlock()
	releases, err := s.publishedReleases(ctx)
	if err != nil {
		return err
	}
	retained := map[string]bool{}
	for _, release := range releases {
		candidate := child(release.Tag)
		if err = candidate.sync(ctx); err != nil {
			if errors.Is(err, errIncompatibleRelease) {
				continue
			}
			return err
		}
		if len(retained) == 0 {
			if err = activate(candidate); err != nil {
				return err
			}
		}
		retained[release.Tag] = true
		if len(retained) >= keep {
			break
		}
	}
	if len(retained) == 0 {
		return errors.New("no published agent releases match this server's protocol and Restic version")
	}
	if purge {
		if err = h.purgeReleaseCache(retained, keep); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.status.State, s.status.Error = "ready", ""
	s.mu.Unlock()
	return nil
}

func cachedRelease(root, tag string) string {
	pointer, err := readBoundedFile(filepath.Join(root, "current"), 128)
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(string(pointer))
	if !cacheNamePattern.MatchString(name) {
		return ""
	}
	directory := filepath.Join(root, name)
	if verifyReleaseDirectory(directory, tag) != nil {
		return ""
	}
	return directory
}

func (s *releaseStore) publishedReleases(ctx context.Context) ([]publishedAgentRelease, error) {
	result := []publishedAgentRelease{}
	for page := 1; page <= 3; page++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/deranjer/vigara/releases?per_page=100&page="+strconv.Itoa(page), nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Accept", "application/vnd.github+json")
		request.Header.Set("User-Agent", "Vigara-agent-release-fetcher")
		response, err := s.client.Do(request)
		if err != nil {
			return nil, errors.New("could not scan GitHub agent releases")
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
		response.Body.Close()
		if readErr != nil || response.StatusCode != 200 || len(data) > 4<<20 {
			return nil, errors.New("GitHub release scan failed or was rate limited; retrying automatically")
		}
		var batch []publishedAgentRelease
		if json.Unmarshal(data, &batch) != nil {
			return nil, errors.New("invalid GitHub releases response")
		}
		for _, release := range batch {
			if !release.Draft && releaseTagPattern.MatchString(release.Tag) && !release.Published.IsZero() {
				result = append(result, release)
			}
		}
		if len(batch) < 100 {
			break
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Published.Equal(result[j].Published) {
			return result[i].Tag > result[j].Tag
		}
		return result[i].Published.After(result[j].Published)
	})
	return result, nil
}

func (h *Handler) purgeReleaseCache(retained map[string]bool, keep int) error {
	s := h.releaseStore
	h.releaseFilesMu.Lock()
	defer h.releaseFilesMu.Unlock()
	// Preserve the active selection, even if an operator explicitly pinned it.
	retained[h.releaseStatus().Tag] = true
	entries, err := os.ReadDir(s.cacheRoot)
	if err != nil {
		return errors.New("could not inspect agent release cache for purging")
	}
	type version struct {
		name     string
		modified time.Time
	}
	versions := []version{}
	for _, entry := range entries {
		if entry.IsDir() && releaseTagPattern.MatchString(entry.Name()) {
			info, e := entry.Info()
			if e != nil {
				return e
			}
			versions = append(versions, version{entry.Name(), info.ModTime()})
		}
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].modified.After(versions[j].modified) })
	for _, v := range versions {
		if len(retained) < keep {
			retained[v.name] = true
		}
	}
	for _, v := range versions {
		if !retained[v.name] {
			// Only exact release-tag directories directly inside the configured
			// cache root qualify; unrelated files and symlinks are untouched.
			if err := os.RemoveAll(filepath.Join(s.cacheRoot, v.name)); err != nil {
				return errors.New("could not purge old agent releases")
			}
		}
	}
	return nil
}
