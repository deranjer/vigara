package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func scheduledReleaseFixture(t *testing.T) (*Handler, map[string]map[string][]byte) {
	t.Helper()
	h, base := releaseFixture(t)
	files := map[string]map[string][]byte{}
	releases := []publishedAgentRelease{}
	for index, letter := range []string{"a", "b", "c"} {
		tag := "agents-sha-" + strings.Repeat(letter, 40)
		assets := map[string][]byte{}
		for name, data := range base {
			assets[name] = append([]byte(nil), data...)
		}
		var manifest map[string]any
		_ = json.Unmarshal(assets["manifest.json"], &manifest)
		manifest["sourceRevision"], manifest["version"] = strings.Repeat(letter, 40), "version-"+letter
		for _, group := range []string{"artifacts", "resticArtifacts"} {
			for _, item := range manifest[group].(map[string]any) {
				entry := item.(map[string]any)
				name := entry["file"].(string)
				assets[name] = append(assets[name], letter...)
				entry["sha256"] = releaseDigest(assets[name])
			}
		}
		assets["manifest.json"], _ = json.Marshal(manifest)
		files[tag] = assets
		releases = append(releases, publishedAgentRelease{Tag: tag, Published: time.Date(2026, 11, 1, index, 0, 0, 0, time.UTC)})
	}
	h.releaseStore.client = &http.Client{Transport: releaseTransport(func(r *http.Request) (*http.Response, error) {
		var data []byte
		if r.URL.Host == "api.github.com" {
			data, _ = json.Marshal(releases)
		} else {
			parts := strings.Split(r.URL.Path, "/")
			tag := parts[len(parts)-2]
			data = files[tag][parts[len(parts)-1]]
		}
		code := 200
		if data == nil {
			code = 404
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(string(data))), Header: make(http.Header)}, nil
	})}
	return h, files
}

func cachedTags(t *testing.T, h *Handler) []string {
	t.Helper()
	entries, err := os.ReadDir(h.releaseStore.cacheRoot)
	if err != nil {
		t.Fatal(err)
	}
	result := []string{}
	for _, entry := range entries {
		if entry.IsDir() && releaseTagPattern.MatchString(entry.Name()) {
			result = append(result, entry.Name())
		}
	}
	return result
}

func TestScheduledReleaseScanSelectsNewestAndKeepsTwo(t *testing.T) {
	h, _ := scheduledReleaseFixture(t)
	if err := os.WriteFile(filepath.Join(h.releaseStore.cacheRoot, "unrelated-file"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := h.RefreshReleases(context.Background(), true, 2); err != nil {
		t.Fatal(err)
	}
	if h.releaseStatus().Tag != "agents-sha-"+strings.Repeat("c", 40) || len(cachedTags(t, h)) != 2 {
		t.Fatal("newest version/two-version retention not applied")
	}
	if _, err := os.Stat(filepath.Join(h.releaseStore.cacheRoot, "unrelated-file")); err != nil {
		t.Fatal("unrelated cache file removed")
	}
	_, version, err := h.verifiedArtifact("linux-amd64")
	if err != nil || version != "version-c" {
		t.Fatalf("wrong active bundle: %s %v", version, err)
	}
	// The selected cache survives restart even though the server-commit fallback
	// was purged. A GitHub outage must not make verified downloads unavailable.
	restarted := &Handler{}
	if err := restarted.ConfigureReleaseSync(h.releaseStore.cacheRoot, "auto", strings.Repeat("a", 40)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := restarted.verifiedArtifact("linux-amd64"); err != nil {
		t.Fatal("cached bundles require a scheduled run after restart")
	}
	restarted.releaseStore.client = &http.Client{Transport: releaseTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") })}
	if err := restarted.RefreshReleases(context.Background(), true, 2); err == nil {
		t.Fatal("scan failure not reported")
	}
	if _, version, err := restarted.verifiedArtifact("linux-amd64"); err != nil || version != "version-c" {
		t.Fatal("offline selected cache unavailable")
	}
}

func TestScheduledReleasePurgeCanBeDisabledOrReduced(t *testing.T) {
	h, _ := scheduledReleaseFixture(t)
	if err := h.RefreshReleases(context.Background(), false, 2); err != nil {
		t.Fatal(err)
	}
	if len(cachedTags(t, h)) != 3 {
		t.Fatal("purge-disabled scan deleted history")
	}
	if err := h.RefreshReleases(context.Background(), true, 1); err != nil {
		t.Fatal(err)
	}
	if len(cachedTags(t, h)) != 1 {
		t.Fatal("one-version retention not applied")
	}
}

func TestScheduledReleaseSkipsIncompatibleProtocol(t *testing.T) {
	h, files := scheduledReleaseFixture(t)
	latest := files["agents-sha-"+strings.Repeat("c", 40)]
	var manifest map[string]any
	_ = json.Unmarshal(latest["manifest.json"], &manifest)
	manifest["protocolVersion"] = "future-version"
	latest["manifest.json"], _ = json.Marshal(manifest)
	if err := h.RefreshReleases(context.Background(), true, 2); err != nil {
		t.Fatal(err)
	}
	if h.releaseStatus().Tag != "agents-sha-"+strings.Repeat("b", 40) {
		t.Fatal("incompatible agent selected")
	}
}

func TestExplicitReleasePinIsHonoredDuringScans(t *testing.T) {
	h, _ := scheduledReleaseFixture(t)
	if err := h.RefreshReleases(context.Background(), true, 2); err != nil {
		t.Fatal(err)
	}
	pinned := &Handler{}
	tag := "agents-sha-" + strings.Repeat("a", 40)
	if err := pinned.ConfigureReleaseSync(h.releaseStore.cacheRoot, tag, ""); err != nil {
		t.Fatal(err)
	}
	pinned.releaseStore.client = h.releaseStore.client
	if err := pinned.RefreshReleases(context.Background(), true, 1); err != nil {
		t.Fatal(err)
	}
	if pinned.releaseStatus().Tag != tag || len(cachedTags(t, pinned)) != 1 {
		t.Fatal("explicit release pin or retention was ignored")
	}
}

func TestScheduledReleaseSwitchDoesNotRaceDownloads(t *testing.T) {
	h, _ := scheduledReleaseFixture(t)
	if err := h.releaseStore.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				if _, _, err := h.verifiedArtifact("linux-amd64"); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	if err := h.RefreshReleases(context.Background(), true, 2); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
}

func TestBundleRetentionSettingsValidation(t *testing.T) {
	for _, raw := range []string{`{}`, `{"purgeOld":true,"keepVersions":0}`, `{"purgeOld":true,"keepVersions":21}`, `{"purgeOld":true,"keepVersions":2,"path":"/tmp"}`} {
		if _, err := ParseReleaseRetentionSettings(json.RawMessage(raw)); err == nil {
			t.Fatalf("invalid settings accepted: %s", raw)
		}
	}
	if value, err := ParseReleaseRetentionSettings(json.RawMessage(`{"purgeOld":true,"keepVersions":2}`)); err != nil || !value.PurgeOld || value.KeepVersions != 2 {
		t.Fatal("default retention rejected")
	}
}
