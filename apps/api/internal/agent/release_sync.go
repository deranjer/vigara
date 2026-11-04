package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/deranjer/vigara/internal/protocol"
	"github.com/deranjer/vigara/internal/restic"
)

var releaseTagPattern = regexp.MustCompile(`^agents-sha-[a-f0-9]{40}$`)
var cacheNamePattern = regexp.MustCompile(`^bundle-[a-zA-Z0-9_-]{1,100}$`)

type releaseSyncStatus struct {
	Enabled bool   `json:"enabled"`
	State   string `json:"state"`
	Tag     string `json:"tag,omitempty"`
	Error   string `json:"error,omitempty"`
}

type releaseStore struct {
	opMu      sync.Mutex
	cacheRoot string
	automatic bool
	mu        sync.RWMutex
	status    releaseSyncStatus
	dir       string
	root      string
	client    *http.Client
}

// ConfigureReleaseSync preserves manually provisioned releases. Automatic mode
// always selects assets built from the server's exact source commit.
func (h *Handler) ConfigureReleaseSync(cache, tag, revision string) error {
	if h.releaseDir != "" || tag == "" || tag == "off" {
		return nil
	}
	automatic := tag == "auto"
	if automatic {
		tag = "agents-sha-" + revision
	}
	if !releaseTagPattern.MatchString(tag) {
		return errors.New("agent release tag requires agents-sha- followed by a full source commit; local images need the VIGIRA_REVISION build argument")
	}
	if cache == "" {
		return errors.New("automatic agent releases require VIGIRA_AGENT_RELEASE_CACHE")
	}
	h.releaseStore = &releaseStore{
		cacheRoot: cache, automatic: automatic,
		root:   filepath.Join(cache, tag),
		status: releaseSyncStatus{Enabled: true, State: "pending", Tag: tag},
		client: &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			host := strings.ToLower(req.URL.Hostname())
			if len(via) >= 8 || req.URL.Scheme != "https" || req.URL.User != nil || (host != "github.com" && !strings.HasSuffix(host, ".githubusercontent.com")) {
				return errors.New("untrusted release redirect")
			}
			return nil
		}},
	}
	// Loading an existing verified cache is independent of scheduling. Disabling
	// automatic scans must not hide previously downloaded bundles after restart.
	tags := []string{tag}
	if automatic {
		if pointer, err := readBoundedFile(filepath.Join(cache, "active-tag"), 128); err == nil {
			selected := strings.TrimSpace(string(pointer))
			if releaseTagPattern.MatchString(selected) {
				tags = append([]string{selected}, tags...)
			}
		}
	}
	for _, selected := range tags {
		root := filepath.Join(cache, selected)
		if directory := cachedRelease(root, selected); directory != "" {
			h.releaseStore.root = root
			h.releaseStore.status.Tag = selected
			h.releaseStore.ready(directory)
			break
		}
	}
	return nil
}

func (h *Handler) artifactDirectory() string {
	if h.releaseDir != "" {
		return h.releaseDir
	}
	if h.releaseStore != nil {
		h.releaseStore.mu.RLock()
		defer h.releaseStore.mu.RUnlock()
		return h.releaseStore.dir
	}
	return ""
}

func (h *Handler) releaseStatus() releaseSyncStatus {
	if h.releaseStore == nil {
		return releaseSyncStatus{State: "manual"}
	}
	h.releaseStore.mu.RLock()
	defer h.releaseStore.mu.RUnlock()
	return h.releaseStore.status
}

func (s *releaseStore) ready(directory string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dir = directory
	s.status.State, s.status.Error = "ready", ""
}

func (s *releaseStore) sync(ctx context.Context) error {
	s.mu.Lock()
	s.status.State, s.status.Error = "downloading", ""
	tag := s.status.Tag
	s.mu.Unlock()
	if pointer, err := readBoundedFile(filepath.Join(s.root, "current"), 128); err == nil {
		name := strings.TrimSpace(string(pointer))
		if cacheNamePattern.MatchString(name) {
			directory := filepath.Join(s.root, name)
			if verifyReleaseDirectory(directory, tag) == nil {
				s.ready(directory)
				return nil
			}
		}
	}
	if err := os.MkdirAll(s.root, 0700); err != nil {
		return errors.New("could not write agent release cache; check service permissions")
	}
	stage, err := os.MkdirTemp(s.root, ".download-")
	if err != nil {
		return errors.New("could not prepare agent release cache")
	}
	defer os.RemoveAll(stage)
	if err := s.download(ctx, tag, "manifest.json", stage, "", 1<<20); err != nil {
		return err
	}
	manifest, err := loadSyncManifest(stage, tag)
	if err != nil {
		return err
	}
	for target := range supportedDownloads {
		for _, entry := range []struct{ file, digest string }{
			{manifest.Artifacts[target].File, manifest.Artifacts[target].SHA256},
			{manifest.ResticArtifacts[target].File, manifest.ResticArtifacts[target].SHA256},
		} {
			if err := s.download(ctx, tag, entry.file, stage, entry.digest, 128<<20); err != nil {
				return err
			}
		}
	}
	if err := s.download(ctx, tag, "restic.LICENSE", stage, manifest.ResticLicenseSHA256, 1<<20); err != nil {
		return err
	}
	if err := verifyReleaseDirectory(stage, tag); err != nil {
		return err
	}
	name := "bundle-" + strings.TrimPrefix(filepath.Base(stage), ".download-")
	directory := filepath.Join(s.root, name)
	if err := os.Rename(stage, directory); err != nil {
		return errors.New("could not publish verified agent cache")
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(directory)
		}
	}()
	pointer, err := os.CreateTemp(s.root, ".current-")
	if err != nil {
		return errors.New("could not save agent cache pointer")
	}
	defer os.Remove(pointer.Name())
	_, writeErr := pointer.WriteString(name + "\n")
	closeErr := pointer.Close()
	if writeErr != nil || closeErr != nil || os.Rename(pointer.Name(), filepath.Join(s.root, "current")) != nil {
		return errors.New("could not save agent cache pointer")
	}
	s.ready(directory)
	published = true
	return nil
}

func (s *releaseStore) download(ctx context.Context, tag, name, directory, digest string, limit int64) error {
	// tag and filenames have already been constrained to the release contract.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://github.com/deranjer/vigara/releases/download/"+tag+"/"+name, nil)
	if err != nil {
		return errors.New("invalid release request")
	}
	req.Header.Set("User-Agent", "Vigara-agent-release-fetcher")
	response, err := s.client.Do(req)
	if err != nil {
		return errors.New("could not reach GitHub agent releases")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub agent release unavailable (HTTP %d)", response.StatusCode)
	}
	file, err := os.OpenFile(filepath.Join(directory, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("could not write downloaded agent asset")
	}
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, limit+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || n == 0 || n > limit {
		return errors.New("agent release asset was empty, oversized or interrupted")
	}
	if digest != "" && hex.EncodeToString(hash.Sum(nil)) != digest {
		return errors.New("agent release checksum verification failed")
	}
	return nil
}

func loadSyncManifest(directory, tag string) (releaseManifest, error) {
	var manifest releaseManifest
	b, err := readBoundedFile(filepath.Join(directory, "manifest.json"), 1<<20)
	if err != nil || json.Unmarshal(b, &manifest) != nil || manifest.SourceRevision != strings.TrimPrefix(tag, "agents-sha-") ||
		manifest.Version == "" || len(manifest.Version) > 80 || !validReleaseDigest(manifest.ResticLicenseSHA256) ||
		len(manifest.Artifacts) != len(supportedDownloads) || len(manifest.ResticArtifacts) != len(supportedDownloads) {
		return manifest, errors.New("agent release manifest is incompatible with this server")
	}
	if manifest.ProtocolVersion != protocol.CurrentVersion || manifest.ResticVersion != restic.PinnedVersion {
		return manifest, errIncompatibleRelease
	}
	for target, filename := range supportedDownloads {
		ext := ""
		if target == "windows-amd64" {
			ext = ".exe"
		}
		a, r := manifest.Artifacts[target], manifest.ResticArtifacts[target]
		if a.File != filename || r.File != "restic-"+target+ext || !validReleaseDigest(a.SHA256) || !validReleaseDigest(r.SHA256) {
			return manifest, errors.New("agent release manifest contains invalid artifacts")
		}
	}
	return manifest, nil
}

func validReleaseDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && strings.ToLower(value) == value
}

func verifyReleaseDirectory(directory, tag string) error {
	m, err := loadSyncManifest(directory, tag)
	if err != nil {
		return err
	}
	for target := range supportedDownloads {
		agent, restic := m.Artifacts[target], m.ResticArtifacts[target]
		if err := verifyCachedFile(filepath.Join(directory, agent.File), agent.SHA256, 128<<20); err != nil {
			return errors.New("cached agent checksum verification failed")
		}
		if err := verifyCachedFile(filepath.Join(directory, restic.File), restic.SHA256, 128<<20); err != nil {
			return errors.New("cached restic checksum verification failed")
		}
	}
	if err := verifyCachedFile(filepath.Join(directory, "restic.LICENSE"), m.ResticLicenseSHA256, 1<<20); err != nil {
		return errors.New("cached restic license checksum verification failed")
	}
	return nil
}

func verifyCachedFile(path, digest string, limit int64) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, limit+1))
	if err != nil || n == 0 || n > limit || hex.EncodeToString(hash.Sum(nil)) != digest {
		return errors.New("cached file checksum mismatch")
	}
	return nil
}
