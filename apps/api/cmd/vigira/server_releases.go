package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

type publishedRelease struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

type releaseStatus struct {
	Version       string             `json:"version"`
	Revision      string             `json:"revision"`
	Staging       bool               `json:"staging,omitempty"`
	Releases      []publishedRelease `json:"releases,omitempty"`
	StagingUpdate string             `json:"stagingUpdate,omitempty"`
	CheckedAt     time.Time          `json:"checkedAt,omitzero"`
	Failed        bool               `json:"failed,omitempty"`
}

type serverReleases struct {
	mu   sync.RWMutex
	last releaseStatus
}

func (s *serverReleases) status() releaseStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v := s.last
	v.Version, v.Revision = version, revision
	v.Staging = isStagingVersion(version)
	return v
}

func isStagingVersion(version string) bool {
	// Tagged releases retain their v prefix, including tagged prereleases.
	_, suffix, _ := strings.Cut(version, "-")
	return version == "staging" || (!strings.HasPrefix(version, "v") && strings.HasPrefix(suffix, "staging."))
}

func githubJSON(ctx context.Context, path string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/deranjer/vigara/"+path, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub returned %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(target)
}

func (s *serverReleases) check(ctx context.Context, _ json.RawMessage) (err error) {
	next := releaseStatus{}
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if err != nil {
			s.last.Failed = true
			return
		}
		next.CheckedAt = time.Now().UTC()
		s.last = next
	}()
	if isStagingVersion(version) && revision != "" {
		var runs struct {
			Runs []struct {
				SHA string `json:"head_sha"`
			} `json:"workflow_runs"`
		}
		if err := githubJSON(ctx, "actions/workflows/container.yml/runs?branch=main&status=success&event=push&per_page=1", &runs); err != nil {
			return err
		}
		if len(runs.Runs) == 0 {
			return fmt.Errorf("no successful staging image build found")
		}
		sha := runs.Runs[0].SHA
		if sha != revision {
			var cmp struct{ Status string }
			if err := githubJSON(ctx, "compare/"+revision+"..."+sha+"?per_page=1", &cmp); err != nil {
				return err
			}
			if cmp.Status == "ahead" {
				next.StagingUpdate = sha
			}
		}
		return nil
	}
	for page := 1; ; page++ {
		var batch []publishedRelease
		if err := githubJSON(ctx, fmt.Sprintf("releases?per_page=100&page=%d", page), &batch); err != nil {
			return err
		}
		for _, r := range batch {
			if len(r.Tag) > 1 && r.Tag[0] == 'v' && !r.Draft && !r.Prerelease {
				next.Releases = append(next.Releases, r)
			}
		}
		if len(batch) < 100 {
			return nil
		}
	}
}
