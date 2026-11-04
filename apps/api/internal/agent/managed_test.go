package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
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
	"github.com/deranjer/vigara/apps/api/internal/backups"
	"github.com/deranjer/vigara/internal/protocol"
)

func TestManagedBackupProvisioningAndRevisionDelivery(t *testing.T) {
	pool, sessions := agentTestService(t)
	ctx := context.Background()
	admin, err := sessions.CreateFirstUser(ctx, "backupadmin", "Backup Admin", "correct horse battery staple", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	manager, err := backups.NewManager(pool, base64.StdEncoding.EncodeToString(make([]byte, 32)), "")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	artifact := []byte("synthetic executable")
	digest := sha256.Sum256(artifact)
	sum := hex.EncodeToString(digest[:])
	for _, name := range []string{"vigara-agent-linux-amd64", "restic-linux-amd64", "restic.LICENSE"} {
		if err = os.WriteFile(filepath.Join(dir, name), artifact, 0600); err != nil {
			t.Fatal(err)
		}
	}
	manifest := `{"version":"test","resticVersion":"0.19.1","artifacts":{"linux-amd64":{"file":"vigara-agent-linux-amd64","sha256":"` + sum + `"}},"resticArtifacts":{"linux-amd64":{"file":"restic-linux-amd64","sha256":"` + sum + `"}}}`
	if err = os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(pool, sessions)
	h.ConfigureDownloads("https://vigara.example", dir)
	h.ConfigureManagedBackups(manager)
	mux := http.NewServeMux()
	h.Register(mux)
	manager.Register(mux, sessions)
	backups.NewHandler(pool, sessions).Register(mux)
	call := func(method, url string, body any, bearer string, user bool) *httptest.ResponseRecorder {
		var data []byte
		if body != nil {
			data, _ = json.Marshal(body)
		}
		req := httptest.NewRequest(method, url, bytes.NewReader(data))
		if user {
			req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: admin.Token})
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	config := backups.ManagedInput{Repository: "/backup/host", SourcePaths: []string{"/home/user/Documents"}, Initialize: true, Enabled: true, Schedule: backups.Schedule{Weekdays: []string{"Monday"}, Time: "02:00", Timezone: "UTC"}}
	w := call("POST", "/api/agent-downloads", agentDownloadRequest{Target: "linux-amd64", Name: "Managed host", Features: []string{"restic.backup"}, Backup: &config}, "", true)
	if w.Code != 200 {
		t.Fatalf("bundle: %d %s", w.Code, w.Body.String())
	}
	z, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var bootstrap struct {
		Token   string `json:"token"`
		Managed bool   `json:"managedBackups"`
	}
	foundRestic := false
	for _, f := range z.File {
		if f.Name == "vigara-restic" {
			foundRestic = true
		}
		if f.Name == "bootstrap.json" {
			r, _ := f.Open()
			b, _ := io.ReadAll(r)
			r.Close()
			if bytes.Contains(b, []byte("sourcePaths")) {
				t.Fatal("private configuration embedded in bootstrap")
			}
			if err = json.Unmarshal(b, &bootstrap); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !bootstrap.Managed || !foundRestic {
		t.Fatal("bundle did not enable managed backups")
	}
	w = call("POST", "/api/agent/enroll", EnrollmentRequest{Token: bootstrap.Token, DisplayName: "Managed host", Capability: protocol.CapabilityReport{ProtocolVersion: "v1", Platform: "linux", Architecture: "amd64", AgentVersion: "test", Capabilities: []string{"restic.backup"}}}, "", false)
	if w.Code != 200 {
		t.Fatalf("enroll: %d %s", w.Code, w.Body.String())
	}
	var enrolled EnrollmentResponse
	json.Unmarshal(w.Body.Bytes(), &enrolled)
	var device string
	if err = pool.QueryRow(ctx, `SELECT device_id::text FROM agent_identities WHERE id=$1`, enrolled.AgentID).Scan(&device); err != nil {
		t.Fatal(err)
	}
	base := "/api/backups/devices/" + device
	w = call("GET", base+"/managed", nil, "", true)
	if w.Code != 200 || strings.Contains(w.Body.String(), "password") {
		t.Fatalf("public settings leaked credentials: %d %s", w.Code, w.Body.String())
	}
	poll := protocol.AgentRequest{ProtocolVersion: "v1", AgentID: enrolled.AgentID, AgentVersion: "test", Platform: "linux", Architecture: "amd64", Capabilities: []string{"restic.backup"}, SupportsManagedBackups: true}
	readPoll := func() protocol.AgentResponse {
		t.Helper()
		w := call("POST", "/api/agent/poll", poll, enrolled.Credential, false)
		if w.Code != 200 {
			t.Fatalf("poll: %d %s", w.Code, w.Body.String())
		}
		var response protocol.AgentResponse
		if err = json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	response := readPoll()
	if response.Job != nil || response.ManagedBackup == nil || response.ManagedBackup.Password == "" || response.ManagedBackup.Revision != 1 {
		t.Fatal("work issued before profile readiness", response)
	}
	password := response.ManagedBackup.Password
	if _, err = pool.Exec(ctx, `UPDATE backup_policies SET next_run_at=now()-interval '2 minutes' WHERE agent_id=$1`, enrolled.AgentID); err != nil {
		t.Fatal(err)
	}
	if _, err = backups.ScheduleOnce(ctx, pool, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	if r := readPoll(); r.Job != nil {
		t.Fatal("job leased before readiness")
	}
	w = call("GET", base+"/managed/recovery", nil, "", true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), password) {
		t.Fatal("recovery export missing password")
	}
	poll.ManagedBackupStatus = &protocol.ManagedBackupStatus{Revision: 1}
	response = readPoll()
	if response.Job == nil {
		t.Fatal("acknowledged configuration did not enable job delivery")
	}
	config.ExpectedRevision = 1
	config.SourcePaths = []string{"/home/user/Documents", "/home/user/Pictures"}
	w = call("PUT", base+"/managed", config, "", true)
	if w.Code != 409 {
		t.Fatal("edited configuration during active backup", w.Code, w.Body.String())
	}
	result := protocol.JobResult{ProtocolVersion: "v1", ID: "managed-result", JobID: response.Job.ID, AttemptToken: response.Job.AttemptToken, AgentID: enrolled.AgentID, ModuleID: "restic.backup", ModuleVersion: "1.0.0", Status: protocol.JobSucceeded, StartedAt: time.Now().UTC(), FinishedAt: time.Now().UTC()}
	w = call("POST", "/api/agent/reports", ReportBatch{Results: []protocol.JobResult{result}}, enrolled.Credential, false)
	if w.Code != 202 {
		t.Fatalf("report: %d %s", w.Code, w.Body.String())
	}
	w = call("PUT", base+"/managed", config, "", true)
	if w.Code != 200 {
		t.Fatalf("edit: %d %s", w.Code, w.Body.String())
	}
	w = call("PUT", base+"/managed", config, "", true)
	if w.Code != 409 {
		t.Fatal("stale edit overwrote newer configuration")
	}
	response = readPoll()
	if response.Job != nil || response.ManagedBackup.Revision != 2 || len(response.ManagedBackup.SourcePaths) != 2 || response.ManagedBackup.Initialize {
		t.Fatal("updated paths not delivered safely", response)
	}
	poll.ManagedBackupStatus = &protocol.ManagedBackupStatus{Revision: 2, ErrorCode: "source_unavailable"}
	readPoll()
	w = call("GET", base+"/managed", nil, "", true)
	if !strings.Contains(w.Body.String(), "source_unavailable") {
		t.Fatal("agent setup error not visible")
	}
	if call("GET", base+"/managed/recovery", nil, "", false).Code != 401 {
		t.Fatal("recovery secret accessible anonymously")
	}
}

func TestBundledResticRejectsTamperedAndUnsupportedArtifacts(t *testing.T) {
	dir := t.TempDir()
	data := []byte("restic executable")
	sum := sha256.Sum256(data)
	manifest := `{"resticVersion":"0.19.1","resticArtifacts":{"linux-amd64":{"file":"restic-linux-amd64","sha256":"` + hex.EncodeToString(sum[:]) + `"}}}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "restic-linux-amd64"), data, 0600); err != nil {
		t.Fatal(err)
	}
	h := &Handler{releaseDir: dir}
	if _, err := h.verifiedRestic("linux-amd64"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.verifiedRestic("../../elsewhere"); err == nil {
		t.Fatal("accepted arbitrary artifact path")
	}
	os.WriteFile(filepath.Join(dir, "restic-linux-amd64"), []byte("tampered"), 0600)
	if _, err := h.verifiedRestic("linux-amd64"); err == nil {
		t.Fatal("accepted modified restic executable")
	}
}

func TestManagedBackupSetupCanBeCorrectedBeforeReadiness(t *testing.T) {
	pool, sessions := agentTestService(t)
	ctx := context.Background()
	admin, err := sessions.CreateFirstUser(ctx, "setupadmin", "Admin", "correct horse battery staple", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := createTestBackupAgent(ctx, t, pool)
	if err != nil {
		t.Fatal(err)
	}
	var device string
	if err = pool.QueryRow(ctx, `SELECT device_id::text FROM agent_identities WHERE id=$1`, agent.agentID).Scan(&device); err != nil {
		t.Fatal(err)
	}
	m, _ := backups.NewManager(pool, base64.StdEncoding.EncodeToString(make([]byte, 32)), "")
	mux := http.NewServeMux()
	m.Register(mux, sessions)
	config := backups.ManagedInput{Repository: "/backup/mistyped", SourcePaths: []string{"/data"}, Password: "wrong-password", Enabled: true, Schedule: backups.Schedule{Weekdays: []string{"Monday"}, Time: "02:00", Timezone: "UTC"}}
	save := func() *httptest.ResponseRecorder {
		b, _ := json.Marshal(config)
		req := httptest.NewRequest("PUT", "/api/backups/devices/"+device+"/managed", bytes.NewReader(b))
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: admin.Token})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	if w := save(); w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	config.ExpectedRevision = 1
	config.Repository = "/backup/correct"
	config.Password = "correct-password"
	if w := save(); w.Code != 200 {
		t.Fatalf("correct setup: %d %s", w.Code, w.Body.String())
	}
	desired, ready, err := m.Poll(ctx, agent.agentID, nil)
	if err != nil || ready || desired.Revision != 2 || desired.Repository != config.Repository || desired.Password != config.Password {
		t.Fatal("correction not provisioned", err)
	}
	if _, ready, err = m.Poll(ctx, agent.agentID, &protocol.ManagedBackupStatus{Revision: 2}); err != nil || !ready {
		t.Fatal("acknowledgement failed", err)
	}
	config.ExpectedRevision = 2
	config.Repository = "/backup/different"
	config.Password = ""
	if w := save(); w.Code != 400 {
		t.Fatal("initialized repository identity was changed", w.Code, w.Body.String())
	}
}
