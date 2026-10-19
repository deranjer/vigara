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

func TestConfigureServerURLsEnforcesTransportPolicy(t *testing.T) {
	h := &Handler{}
	rejected := h.ConfigureServerURLs("http://192.168.1.97:8080/", []string{"https://vigira.example", "http://100.101.102.103:8080", "http://vigira.example", "https://vigira.example"}, false)
	if len(rejected) != 3 {
		t.Fatalf("without opt-in rejected %d URLs, want 3 plain-HTTP entries: %v", len(rejected), rejected)
	}
	if got := h.serverURLs; len(got) != 1 || got[0] != "https://vigira.example" {
		t.Fatalf("serverURLs = %v", got)
	}
	rejected = h.ConfigureServerURLs("http://192.168.1.97:8080/", []string{"http://100.101.102.103:8080", "http://vigira.example", "https://vigira.example"}, true)
	if len(rejected) != 1 || !strings.Contains(rejected[0].Error(), "vigira.example") {
		t.Fatalf("opt-in must still reject plain HTTP to a public host: %v", rejected)
	}
	want := []string{"http://192.168.1.97:8080", "http://100.101.102.103:8080", "https://vigira.example"}
	if strings.Join(h.serverURLs, ",") != strings.Join(want, ",") {
		t.Fatalf("serverURLs = %v, want %v", h.serverURLs, want)
	}
}

func TestSelectServerURLOnlyReturnsConfiguredURLs(t *testing.T) {
	h := &Handler{}
	h.ConfigureServerURLs("https://primary.example", []string{"http://192.168.1.97:8080"}, true)
	for requested, want := range map[string]string{"": "https://primary.example", "http://192.168.1.97:8080/": "http://192.168.1.97:8080", " https://primary.example ": "https://primary.example"} {
		if got, ok := h.selectServerURL(requested); !ok || got != want {
			t.Errorf("selectServerURL(%q) = %q, %v; want %q", requested, got, ok, want)
		}
	}
	for _, requested := range []string{"https://attacker.example", "http://192.168.1.98:8080", "http://192.168.1.97:8081"} {
		if _, ok := h.selectServerURL(requested); ok {
			t.Errorf("selectServerURL accepted unconfigured URL %q", requested)
		}
	}
	if _, ok := (&Handler{}).selectServerURL(""); ok {
		t.Error("selected a URL when none are configured")
	}
}

func TestPrivateHTTPBundleCarriesInsecureMarkerAndOnlyApprovedURLs(t *testing.T) {
	pool, sessions := agentTestService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := sessions.CreateFirstUser(ctx, "lan-operator", "LAN Operator", "correct horse battery staple", "correct horse battery staple")
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
	handler.ConfigureDownloads("", releaseDir)
	if rejected := handler.ConfigureServerURLs("http://192.168.1.97:8080", []string{"http://100.101.102.103:8080", "https://vigira.example"}, true); len(rejected) != 0 {
		t.Fatalf("unexpected rejected URLs: %v", rejected)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/agent-downloads", strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: admin.Token})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	bootstrapFor := func(rec *httptest.ResponseRecorder) (bootstrap struct {
		ServerURL    string `json:"serverUrl"`
		InsecureHTTP bool   `json:"insecureHttp"`
	}, installer string) {
		t.Helper()
		archive, e := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
		if e != nil {
			t.Fatal(e)
		}
		for _, file := range archive.File {
			reader, e := file.Open()
			if e != nil {
				t.Fatal(e)
			}
			data, e := io.ReadAll(reader)
			_ = reader.Close()
			if e != nil {
				t.Fatal(e)
			}
			switch file.Name {
			case "bootstrap.json":
				if e = json.Unmarshal(data, &bootstrap); e != nil {
					t.Fatal(e)
				}
			case "install.sh":
				installer = string(data)
			}
		}
		return
	}

	availability := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/agent-downloads/availability", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: admin.Token})
	mux.ServeHTTP(availability, req)
	var offered struct {
		ServerURL         string `json:"serverUrl"`
		AllowInsecureHTTP bool   `json:"allowInsecureHttp"`
		ServerURLs        []struct {
			URL    string `json:"url"`
			Secure bool   `json:"secure"`
		} `json:"serverUrls"`
	}
	if availability.Code != http.StatusOK || json.Unmarshal(availability.Body.Bytes(), &offered) != nil {
		t.Fatalf("availability returned %d: %s", availability.Code, availability.Body.String())
	}
	if offered.ServerURL != "http://192.168.1.97:8080" || !offered.AllowInsecureHTTP || len(offered.ServerURLs) != 3 || offered.ServerURLs[0].Secure || !offered.ServerURLs[2].Secure {
		t.Fatalf("availability = %+v", offered)
	}

	// Default URL: plain HTTP on a private address, flagged for the installer.
	rec := post(`{"target":"linux-amd64","name":"LAN node","features":[]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("default bundle returned %d: %s", rec.Code, rec.Body.String())
	}
	bootstrap, installer := bootstrapFor(rec)
	if bootstrap.ServerURL != "http://192.168.1.97:8080" || !bootstrap.InsecureHTTP || !strings.Contains(installer, "--allow-insecure-private-http") {
		t.Fatalf("insecure bootstrap = %+v installer flag present=%v", bootstrap, strings.Contains(installer, "--allow-insecure-private-http"))
	}

	// The agent binary must be installed to a path the service account can execute
	// before enrollment, because the extracted bundle may sit in a private home directory.
	if install, enroll := strings.Index(installer, "install -m 0755"), strings.Index(installer, " enroll "); install < 0 || enroll < 0 || install > enroll || strings.Contains(installer, `sudo -u vigira-agent "$BASE/`) {
		t.Fatalf("installer must install the binary to /usr/local/bin before enrolling from there: %s", installer)
	}

	// An approved HTTPS alternate is not flagged.
	rec = post(`{"target":"linux-amd64","serverUrl":"https://vigira.example/","name":"TLS node","features":[]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("https bundle returned %d: %s", rec.Code, rec.Body.String())
	}
	if bootstrap, _ = bootstrapFor(rec); bootstrap.ServerURL != "https://vigira.example" || bootstrap.InsecureHTTP {
		t.Fatalf("https bootstrap = %+v", bootstrap)
	}

	// Clients cannot choose a URL the operator did not configure.
	if rec = post(`{"target":"linux-amd64","serverUrl":"http://203.0.113.9:8080","name":"Rogue","features":[]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unapproved URL returned %d, want 400", rec.Code)
	}
}
