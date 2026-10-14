package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/internal/protocol"
)

func TestVerifiedArtifactUsesFixedNamesAndDigest(t *testing.T) {
	dir := t.TempDir()
	data := []byte("synthetic agent binary")
	digest := sha256.Sum256(data)
	manifest := releaseManifest{Version: "test", Artifacts: map[string]struct {
		File   string `json:"file"`
		SHA256 string `json:"sha256"`
	}{
		"linux-amd64": {File: supportedDownloads["linux-amd64"], SHA256: hex.EncodeToString(digest[:])},
	}}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "manifest.json"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, supportedDownloads["linux-amd64"]), data, 0600); err != nil {
		t.Fatal(err)
	}
	h := &Handler{releaseDir: dir}
	got, version, err := h.verifiedArtifact("linux-amd64")
	if err != nil || version != "test" || string(got) != string(data) {
		t.Fatalf("verified artifact = %q, %q, %v", got, version, err)
	}
	if _, _, err = h.verifiedArtifact("../../manifest.json"); err == nil {
		t.Fatal("accepted caller-controlled target path")
	}
	if err = os.WriteFile(filepath.Join(dir, supportedDownloads["linux-amd64"]), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = h.verifiedArtifact("linux-amd64"); err == nil {
		t.Fatal("accepted artifact with mismatched digest")
	}
}

func TestVerifiedArtifactRejectsManifestPathOverride(t *testing.T) {
	dir := t.TempDir()
	manifest := releaseManifest{Version: "test", Artifacts: map[string]struct {
		File   string `json:"file"`
		SHA256 string `json:"sha256"`
	}{"linux-amd64": {File: "../../outside", SHA256: "0000000000000000000000000000000000000000000000000000000000000000"}}}
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := (&Handler{releaseDir: dir}).verifiedArtifact("linux-amd64"); err == nil {
		t.Fatal("accepted manifest path override")
	}
}

func TestAgentDownloadCancelRejectsBootstrapToken(t *testing.T) {
	pool, sessions := agentTestService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := sessions.CreateFirstUser(ctx, "download-operator", "Download Operator", "correct horse battery staple", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	releaseDir := t.TempDir()
	artifact := []byte("synthetic agent executable")
	digest := sha256.Sum256(artifact)
	if err = os.WriteFile(filepath.Join(releaseDir, supportedDownloads["linux-amd64"]), artifact, 0600); err != nil {
		t.Fatal(err)
	}
	manifest := `{"version":"test","artifacts":{"linux-amd64":{"file":"` + supportedDownloads["linux-amd64"] + `","sha256":"` + hex.EncodeToString(digest[:]) + `"}}}`
	if err = os.WriteFile(filepath.Join(releaseDir, "manifest.json"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(pool, sessions)
	handler.ConfigureDownloads("https://vigira.example", releaseDir)
	mux := http.NewServeMux()
	handler.Register(mux)
	create := httptest.NewRequest(http.MethodPost, "/api/agent-downloads", strings.NewReader(`{"target":"linux-amd64","name":"Synthetic node","features":[]}`))
	create.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: admin.Token})
	created := httptest.NewRecorder()
	mux.ServeHTTP(created, create)
	if created.Code != http.StatusOK || created.Header().Get("Cache-Control") != "no-store, private" {
		t.Fatalf("bundle returned %d headers=%v body=%s", created.Code, created.Header(), created.Body.String())
	}
	sessionID := created.Header().Get("X-Vigira-Session-ID")
	if sessionID == "" {
		t.Fatal("bundle response omitted session ID")
	}
	archive, err := zip.NewReader(bytes.NewReader(created.Body.Bytes()), int64(created.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var bootstrap struct {
		Token string `json:"token"`
	}
	installerFound := false
	for _, file := range archive.File {
		if file.Name == "install.sh" {
			reader, e := file.Open()
			if e != nil {
				t.Fatal(e)
			}
			script, e := io.ReadAll(reader)
			_ = reader.Close()
			if e != nil {
				t.Fatal(e)
			}
			installerFound = strings.Contains(string(script), `printf '%s\n' "$TOKEN" | sudo -u`) && strings.Contains(string(script), `rm -f -- "$CONFIG"`)
		}
		if file.Name == "bootstrap.json" {
			reader, e := file.Open()
			if e != nil {
				t.Fatal(e)
			}
			data, e := io.ReadAll(reader)
			_ = reader.Close()
			if e != nil {
				t.Fatal(e)
			}
			if e = json.Unmarshal(data, &bootstrap); e != nil {
				t.Fatal(e)
			}
		}
	}
	if bootstrap.Token == "" {
		t.Fatal("bundle omitted its one-use token")
	}
	if !installerFound {
		t.Fatal("Linux installer must pipe the token through stdin and remove bootstrap data")
	}
	status := httptest.NewRequest(http.MethodGet, "/api/agent-downloads/"+sessionID, nil)
	status.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: admin.Token})
	statusResult := httptest.NewRecorder()
	mux.ServeHTTP(statusResult, status)
	if statusResult.Code != http.StatusOK || !strings.Contains(statusResult.Body.String(), `"status":"waiting"`) {
		t.Fatalf("initial session status %d: %s", statusResult.Code, statusResult.Body.String())
	}
	request := httptest.NewRequest(http.MethodPost, "/api/agent-downloads/"+sessionID+"/cancel", nil)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: admin.Token})
	canceled := httptest.NewRecorder()
	mux.ServeHTTP(canceled, request)
	if canceled.Code != http.StatusNoContent {
		t.Fatalf("cancel returned %d: %s", canceled.Code, canceled.Body.String())
	}
	enrollment, _ := json.Marshal(EnrollmentRequest{Token: bootstrap.Token, DisplayName: "Synthetic node", Capability: protocol.CapabilityReport{ProtocolVersion: protocol.CurrentVersion, Platform: "linux", Architecture: "amd64", AgentVersion: "test"}})
	replay := httptest.NewRequest(http.MethodPost, "/api/agent/enroll", bytes.NewReader(enrollment))
	replayResult := httptest.NewRecorder()
	mux.ServeHTTP(replayResult, replay)
	if replayResult.Code != http.StatusUnauthorized {
		t.Fatalf("canceled bootstrap token enrollment status=%d body=%s", replayResult.Code, replayResult.Body.String())
	}
}
