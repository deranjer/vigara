package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deranjer/vigara/internal/protocol"
	"github.com/deranjer/vigara/internal/restic"
)

type releaseTransport func(*http.Request) (*http.Response, error)

func (f releaseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func releaseDigest(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func releaseFixture(t *testing.T) (*Handler, map[string][]byte) {
	t.Helper()
	revision := strings.Repeat("a", 40)
	h := &Handler{}
	if err := h.ConfigureReleaseSync(t.TempDir(), "auto", revision); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{"restic.LICENSE": []byte("test license")}
	artifacts, resticArtifacts := map[string]any{}, map[string]any{}
	for target, filename := range supportedDownloads {
		files[filename] = []byte("agent:" + target)
		artifacts[target] = map[string]string{"file": filename, "sha256": releaseDigest(files[filename])}
		resticName := "restic-" + target
		if target == "windows-amd64" {
			resticName += ".exe"
		}
		files[resticName] = []byte("restic:" + target)
		resticArtifacts[target] = map[string]string{"file": resticName, "sha256": releaseDigest(files[resticName])}
	}
	files["manifest.json"], _ = json.Marshal(map[string]any{
		"version": "test-sha", "sourceRevision": revision, "protocolVersion": protocol.CurrentVersion, "resticVersion": restic.PinnedVersion,
		"resticLicenseSha256": releaseDigest(files["restic.LICENSE"]), "artifacts": artifacts, "resticArtifacts": resticArtifacts,
	})
	h.releaseStore.client = &http.Client{Transport: releaseTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "github.com" || !strings.Contains(r.URL.Path, "/releases/download/agents-sha-"+revision+"/") {
			t.Errorf("unexpected release request: %s", r.URL)
		}
		data, ok := files[filepath.Base(r.URL.Path)]
		code := 200
		if !ok {
			code = 404
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(string(data))), Header: make(http.Header)}, nil
	})}
	return h, files
}

func TestReleaseFetchPublishesCompleteVerifiedCache(t *testing.T) {
	h, _ := releaseFixture(t)
	client := h.releaseStore.client.Transport
	h.releaseStore.client.Transport = releaseTransport(func(r *http.Request) (*http.Response, error) {
		if h.artifactDirectory() != "" {
			t.Fatal("partially downloaded assets became available")
		}
		return client.RoundTrip(r)
	})
	if err := h.releaseStore.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.releaseStatus().State != "ready" {
		t.Fatal("release was not marked ready")
	}
	for target := range supportedDownloads {
		if _, _, err := h.verifiedArtifact(target); err != nil {
			t.Fatal(err)
		}
		if _, err := h.verifiedRestic(target); err != nil {
			t.Fatal(err)
		}
	}
	// Restart with no network. A pinned, verified cache must still be available.
	restarted := &Handler{}
	if err := restarted.ConfigureReleaseSync(filepath.Dir(h.releaseStore.root), h.releaseStatus().Tag, ""); err != nil {
		t.Fatal(err)
	}
	restarted.releaseStore.client = &http.Client{Transport: releaseTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("verified offline cache unexpectedly requested the network")
		return nil, errors.New("offline")
	})}
	if err := restarted.releaseStore.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if restarted.artifactDirectory() != h.artifactDirectory() {
		t.Fatal("offline restart did not reuse the complete cache")
	}
}

func TestReleaseFetchRejectsBadAssetsAndRetries(t *testing.T) {
	for _, name := range []string{"vigara-agent-linux-amd64", "restic-linux-arm64", "restic.LICENSE"} {
		t.Run(name, func(t *testing.T) {
			h, files := releaseFixture(t)
			good := files[name]
			files[name] = []byte("corrupt binary")
			if err := h.releaseStore.sync(context.Background()); err == nil {
				t.Fatal("corrupted asset accepted")
			}
			if h.artifactDirectory() != "" {
				t.Fatal("failed release became available")
			}
			leftovers, _ := filepath.Glob(filepath.Join(h.releaseStore.root, ".download-*"))
			if len(leftovers) != 0 {
				t.Fatal("interrupted staging directory retained")
			}
			files[name] = good
			if err := h.releaseStore.sync(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReleaseManifestRejectsIncompatibleOrUnsafeFiles(t *testing.T) {
	for _, change := range []func(map[string]any){
		func(m map[string]any) { m["sourceRevision"] = strings.Repeat("b", 40) },
		func(m map[string]any) { m["resticVersion"] = "unsupported" },
		func(m map[string]any) { m["resticLicenseSha256"] = "invalid" },
		func(m map[string]any) {
			m["artifacts"].(map[string]any)["linux-amd64"].(map[string]any)["file"] = "../../outside"
		},
		func(m map[string]any) { delete(m["artifacts"].(map[string]any), "linux-arm64") },
	} {
		h, files := releaseFixture(t)
		var manifest map[string]any
		_ = json.Unmarshal(files["manifest.json"], &manifest)
		change(manifest)
		files["manifest.json"], _ = json.Marshal(manifest)
		if err := h.releaseStore.sync(context.Background()); err == nil {
			t.Fatal("invalid release manifest accepted")
		}
	}
}

func TestReleaseFetchMissingAssetsAndSizeLimit(t *testing.T) {
	h, files := releaseFixture(t)
	delete(files, "manifest.json")
	if err := h.releaseStore.sync(context.Background()); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatal("missing release did not produce a retryable error")
	}
	files["test-file"] = []byte("too much content")
	if err := h.releaseStore.download(context.Background(), h.releaseStatus().Tag, "test-file", t.TempDir(), "", 3); err == nil {
		t.Fatal("oversized download accepted")
	}
}

func TestReleaseCacheCorruptionIsReplacedByVerifiedDownload(t *testing.T) {
	h, _ := releaseFixture(t)
	if err := h.releaseStore.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	old := h.artifactDirectory()
	if err := os.WriteFile(filepath.Join(old, supportedDownloads["linux-amd64"]), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := h.releaseStore.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if old == h.artifactDirectory() {
		t.Fatal("corrupted cache was not replaced")
	}
	if _, _, err := h.verifiedArtifact("linux-amd64"); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseConfigurationAndRedirectPolicy(t *testing.T) {
	h := &Handler{}
	if h.ConfigureReleaseSync(t.TempDir(), "../other", "") == nil || h.ConfigureReleaseSync(t.TempDir(), "auto", "") == nil {
		t.Fatal("invalid release tag accepted")
	}
	if err := h.ConfigureReleaseSync(t.TempDir(), "auto", strings.Repeat("a", 40)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"http://github.com/file", "https://evil.example/file", "https://user@github.com/file"} {
		u, _ := url.Parse(raw)
		if h.releaseStore.client.CheckRedirect(&http.Request{URL: u}, nil) == nil {
			t.Fatalf("untrusted redirect accepted: %s", raw)
		}
	}
	u, _ := url.Parse("https://release-assets.githubusercontent.com/file")
	if err := h.releaseStore.client.CheckRedirect(&http.Request{URL: u}, nil); err != nil {
		t.Fatal(err)
	}
	manual := &Handler{releaseDir: "operator-owned"}
	if err := manual.ConfigureReleaseSync(t.TempDir(), "auto", ""); err != nil || manual.releaseStore != nil {
		t.Fatal("manual release directory lost precedence")
	}
}

// The publishing workflow runs this against the actual cross-compiled assets,
// not fixture bytes, before uploading them to a draft GitHub release.
func TestBuiltReleaseDirectory(t *testing.T) {
	directory := os.Getenv("VIGIRA_TEST_AGENT_RELEASE_DIR")
	if directory == "" {
		t.Skip("set VIGIRA_TEST_AGENT_RELEASE_DIR to validate built release assets")
	}
	data, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest releaseManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if err := verifyReleaseDirectory(directory, "agents-sha-"+manifest.SourceRevision); err != nil {
		t.Fatal(err)
	}
}
