package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/deranjer/vigara/internal/protocol"
)

func TestHostBundleRejectsInvalidOptionsWithoutOutput(t *testing.T) {
	valid := HostAgentOptions{Name: "host", Target: "linux-amd64", ServerURL: "http://127.0.0.1:8080", DockerHost: "unix:///var/run/docker.sock", ManagedRoot: "/srv/compose"}
	for _, mutate := range []func(*HostAgentOptions){
		func(o *HostAgentOptions) { o.Target = "windows-amd64" },
		func(o *HostAgentOptions) { o.DockerHost = "http://127.0.0.1:2375" },
		func(o *HostAgentOptions) { o.ServerURL = "http://203.0.113.9:8080" },
		func(o *HostAgentOptions) { o.ManagedRoot = "/srv/../etc" },
		func(o *HostAgentOptions) { o.DockerHost = "unix:///var/run/docker.sock\n" },
	} {
		options := valid
		mutate(&options)
		var output bytes.Buffer
		if err := (&Handler{}).WriteHostAgentBundle(context.Background(), &output, options); err == nil || output.Len() != 0 {
			t.Fatalf("invalid options accepted or produced bundle: %+v, %v", options, err)
		}
	}
}

func TestHostBundleEnrollsWithDeployScopeAndCannotReplay(t *testing.T) {
	pool, service := agentTestService(t)
	dir := t.TempDir()
	artifact := []byte("verified host agent")
	digest := sha256.Sum256(artifact)
	manifest := `{"version":"test","artifacts":{"linux-amd64":{"file":"vigara-agent-linux-amd64","sha256":"` + hex.EncodeToString(digest[:]) + `"}}}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vigara-agent-linux-amd64"), artifact, 0600); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(pool, service)
	h.ConfigureDownloads("https://public.example", dir)
	options := HostAgentOptions{Name: "Server host", Target: "linux-amd64", ServerURL: "http://127.0.0.1:8080", DockerHost: "unix:///var/run/docker.sock", ManagedRoot: "/srv/shared stacks"}
	var output bytes.Buffer
	if err := h.WriteHostAgentBundle(context.Background(), &output, options); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(h.serverURLs, []string{"https://public.example"}) {
		t.Fatal("local bundle changed public download origins")
	}
	archive, err := zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.File) != 7 {
		t.Fatalf("unexpected bundle files: %d", len(archive.File))
	}
	var bootstrap struct {
		Token, Name, ServerURL, SessionID string
		Docker                            *dockerSetup
	}
	for _, entry := range archive.File {
		if entry.Name == "bootstrap.json" {
			file, err := entry.Open()
			if err != nil {
				t.Fatal(err)
			}
			err = json.NewDecoder(file).Decode(&bootstrap)
			file.Close()
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if bootstrap.Token == "" || bootstrap.ServerURL != options.ServerURL || bootstrap.Docker == nil || bootstrap.Docker.Mode != "deploy" || bootstrap.Docker.ManagedRoot != options.ManagedRoot {
		t.Fatal("bundle omitted host enrollment or deployment settings")
	}
	var capabilities []string
	var expiresSoon bool
	if err := pool.QueryRow(context.Background(), `SELECT capabilities,expires_at > now() AND expires_at <= now()+interval '31 minutes' FROM agent_enrollment_tokens WHERE id=$1`, bootstrap.SessionID).Scan(&capabilities, &expiresSoon); err != nil {
		t.Fatal(err)
	}
	want, _ := protocol.DockerTierCapabilities(protocol.DockerModeDeploy)
	if !slices.Equal(capabilities, want) || !expiresSoon {
		t.Fatalf("wrong enrollment scope/lifetime: %v, %v", capabilities, expiresSoon)
	}
	var duplicate bytes.Buffer
	if err := h.WriteHostAgentBundle(context.Background(), &duplicate, options); err == nil || duplicate.Len() != 0 {
		t.Fatal("duplicate host name was accepted")
	}
	enrollment, _ := json.Marshal(EnrollmentRequest{Token: bootstrap.Token, DisplayName: options.Name, Capability: protocol.CapabilityReport{ProtocolVersion: protocol.CurrentVersion, Platform: "linux", Architecture: "amd64", AgentVersion: "test", Capabilities: want}})
	mux := http.NewServeMux()
	h.Register(mux)
	for _, status := range []int{http.StatusOK, http.StatusUnauthorized} {
		result := httptest.NewRecorder()
		mux.ServeHTTP(result, httptest.NewRequest(http.MethodPost, "/api/agent/enroll", bytes.NewReader(enrollment)))
		if result.Code != status {
			t.Fatalf("enrollment status %d, want %d: %s", result.Code, status, result.Body.String())
		}
	}
	unauthorized := httptest.NewRecorder()
	mux.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/api/agent-downloads", bytes.NewBufferString(`{"name":"unauthorized"}`)))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("public download auth changed: %d", unauthorized.Code)
	}
}
