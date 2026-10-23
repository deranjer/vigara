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

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/apps/api/internal/inventory"
	"github.com/deranjer/vigara/internal/protocol"
)

func TestReinstallBundleKeepsIdentityAndRotatesOnlyOnUse(t *testing.T) {
	pool, sessions := agentTestService(t)
	ctx := context.Background()
	admin, err := sessions.CreateFirstUser(ctx, "reinstall-admin", "Admin", "correct horse battery staple", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := createTestBackupAgent(ctx, t, pool)
	if err != nil {
		t.Fatal(err)
	}
	var deviceID string
	if err = pool.QueryRow(ctx, "SELECT device_id::text FROM agent_identities WHERE id=$1", identity.agentID).Scan(&deviceID); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = inventory.AttachSource(ctx, tx, deviceID, "agent", identity.agentID, inventory.SourceProvenance("agent", identity.agentID)); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	jobID, err := createTestBackupJob(ctx, pool, identity.agentID)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	data := []byte("synthetic windows agent")
	digest := sha256.Sum256(data)
	if err = os.WriteFile(filepath.Join(dir, supportedDownloads["linux-amd64"]), data, 0600); err != nil {
		t.Fatal(err)
	}
	manifest := `{"version":"test","artifacts":{"linux-amd64":{"file":"vigara-agent-linux-amd64","sha256":"` + hex.EncodeToString(digest[:]) + `"}}}`
	if err = os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(pool, sessions)
	handler.ConfigureDownloads("https://vigara.example", dir)
	mux := http.NewServeMux()
	handler.Register(mux)
	call := func(method, path, body, cookie string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookie})
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	duplicate := call("POST", "/api/agent-downloads", `{"name":" BACKUP-COORDINATOR-TEST ","target":"linux-amd64","features":[]}`, admin.Token)
	if duplicate.Code != 409 {
		t.Fatalf("duplicate=%d %s", duplicate.Code, duplicate.Body.String())
	}
	request := `{"deviceId":"` + deviceID + `","name":"ignored label","target":"linux-amd64","features":[]}`
	bundle := call("POST", "/api/agent-downloads", request, admin.Token)
	if bundle.Code != 200 {
		t.Fatalf("bundle=%d %s", bundle.Code, bundle.Body.String())
	}
	bootstrap := func(w *httptest.ResponseRecorder) string {
		t.Helper()
		zr, e := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
		if e != nil {
			t.Fatal(e)
		}
		for _, f := range zr.File {
			if f.Name == "bootstrap.json" {
				r, e := f.Open()
				if e != nil {
					t.Fatal(e)
				}
				raw, e := io.ReadAll(r)
				r.Close()
				if e != nil {
					t.Fatal(e)
				}
				var b struct {
					Token     string
					Reinstall bool
					AgentID   string
				}
				if e = json.Unmarshal(raw, &b); e != nil {
					t.Fatal(e)
				}
				if !b.Reinstall || b.AgentID != identity.agentID {
					t.Fatalf("incorrect recovery binding: %+v", b)
				}
				return b.Token
			}
		}
		t.Fatal("missing bootstrap")
		return ""
	}
	staleToken := bootstrap(bundle)
	newer := call("POST", "/api/agent-downloads", request, admin.Token)
	if newer.Code != 200 {
		t.Fatalf("newer bundle=%d %s", newer.Code, newer.Body.String())
	}
	token := bootstrap(newer)
	var oldHash []byte
	if err = pool.QueryRow(ctx, "SELECT credential_hash FROM agent_identities WHERE id=$1", identity.agentID).Scan(&oldHash); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(oldHash, hash(identity.credential)) {
		t.Fatal("download rotated credentials before installation")
	}
	enroll := func(token, platform string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(EnrollmentRequest{Token: token, DisplayName: "ignored label", Capability: protocol.CapabilityReport{ProtocolVersion: protocol.CurrentVersion, Platform: platform, Architecture: "amd64", AgentVersion: "test", Capabilities: []string{"restic.backup"}}})
		return call("POST", "/api/agent/enroll", string(body), "")
	}
	if w := enroll(staleToken, "linux"); w.Code != 401 {
		t.Fatalf("superseded token=%d %s", w.Code, w.Body.String())
	}
	if w := enroll(token, "windows"); w.Code != 409 {
		t.Fatalf("wrong platform=%d %s", w.Code, w.Body.String())
	}
	enrolled := enroll(token, "linux")
	if enrolled.Code != 200 {
		t.Fatalf("reinstall=%d %s", enrolled.Code, enrolled.Body.String())
	}
	var result EnrollmentResponse
	if err = json.Unmarshal(enrolled.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.AgentID != identity.agentID || result.Credential == identity.credential {
		t.Fatal("reinstall did not preserve identity and replace credential")
	}
	if w := enroll(token, "linux"); w.Code != 401 {
		t.Fatalf("replayed token=%d", w.Code)
	}
	var count int
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM devices").Scan(&count); err != nil || count != 1 {
		t.Fatalf("device count=%d err=%v", count, err)
	}
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM backup_jobs WHERE id=$1", jobID).Scan(&count); err != nil || count != 1 {
		t.Fatal("backup history lost")
	}
	for _, tc := range []struct {
		credential string
		want       int
	}{{identity.credential, 401}, {result.Credential, 200}} {
		payload, _ := json.Marshal(protocol.AgentRequest{ProtocolVersion: protocol.CurrentVersion, AgentID: identity.agentID, Platform: "linux", Architecture: "amd64", AgentVersion: "test"})
		r := httptest.NewRequest("POST", "/api/agent/poll", bytes.NewReader(payload))
		r.Header.Set("Authorization", "Bearer "+tc.credential)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("poll=%d want %d: %s", w.Code, tc.want, w.Body.String())
		}
	}
}

func TestDeleteDeviceRequiresAdminExactNameAndNoActiveWork(t *testing.T) {
	pool, sessions := agentTestService(t)
	ctx := context.Background()
	admin, err := sessions.CreateFirstUser(ctx, "delete-admin", "Admin", "correct horse battery staple", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := sessions.CreateUser(ctx, "delete-viewer", "Viewer", "correct horse battery staple", auth.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	login, err := sessions.Login(ctx, viewer.Username, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := createTestBackupAgent(ctx, t, pool)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	if err = pool.QueryRow(ctx, "SELECT device_id::text FROM agent_identities WHERE id=$1", agent.agentID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	job, err := createTestBackupJob(ctx, pool, agent.agentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "UPDATE backup_jobs SET status='leased' WHERE id=$1", job); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "INSERT INTO backup_job_attempts(job_id,attempt_number,lease_expires_at) VALUES($1,1,now()+interval '1 hour')", job); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(pool, sessions)
	mux := http.NewServeMux()
	handler.Register(mux)
	remove := func(name, token string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"name": name})
		r := httptest.NewRequest("DELETE", "/api/devices/"+id, bytes.NewReader(body))
		if token != "" {
			r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct {
		name, token string
		want        int
	}{{"backup-coordinator-test", "", 401}, {"backup-coordinator-test", login.Token, 403}, {"Backup-coordinator-test", admin.Token, 409}, {"backup-coordinator-test", admin.Token, 409}} {
		w := remove(tc.name, tc.token)
		if w.Code != tc.want {
			t.Fatalf("delete=%d want=%d: %s", w.Code, tc.want, w.Body.String())
		}
	}
	if _, err = pool.Exec(ctx, "UPDATE backup_jobs SET status='completed' WHERE id=$1", job); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "INSERT INTO stack_bindings(stack_id,agent_id,project_name) VALUES('delete-stack',$1,'delete-stack')", agent.agentID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO stack_deployments(id,stack_id,agent_id,project_name,actor_name,commit_id,configuration_digest,snapshot,plan_token,status) VALUES('delete-run','delete-stack',$1,'delete-stack','test','abc','digest','{}','test','succeeded')`, agent.agentID); err != nil {
		t.Fatal(err)
	}
	w := remove("backup-coordinator-test", admin.Token)
	if w.Code != 204 {
		t.Fatalf("delete=%d: %s", w.Code, w.Body.String())
	}
	for _, table := range []string{"devices", "agent_identities", "backup_policies", "backup_jobs", "backup_job_attempts", "stack_bindings", "stack_deployments"} {
		var count int
		if err = pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
	var count int
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE action='device.delete' AND target_id=$1", id).Scan(&count); err != nil || count != 1 {
		t.Fatalf("audit count=%d err=%v", count, err)
	}
	if w = remove("backup-coordinator-test", admin.Token); w.Code != 404 {
		t.Fatalf("deleted device=%d", w.Code)
	}
}
