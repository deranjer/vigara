package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/internal/protocol"
	"github.com/deranjer/vigara/internal/restic"
)

func updateRelease(t *testing.T) (string, protocol.AgentUpdatePlan) {
	t.Helper()
	dir := t.TempDir()
	data := []byte("test updater-capable executable")
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	manifest := map[string]any{"version": "new", "selfUpdateProtocol": "v1", "protocolVersion": protocol.CurrentVersion, "resticVersion": restic.PinnedVersion, "artifacts": map[string]any{"linux-amd64": map[string]string{"file": supportedDownloads["linux-amd64"], "sha256": digest}}}
	encoded, _ := json.Marshal(manifest)
	for name, body := range map[string][]byte{"manifest.json": encoded, supportedDownloads["linux-amd64"]: data} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir, protocol.AgentUpdatePlan{Version: "new", SHA256: digest, Size: int64(len(data))}
}

func TestUpdateRejectsIncompatibleAndLegacyPackages(t *testing.T) {
	for _, field := range []string{"selfUpdateProtocol", "protocolVersion", "resticVersion"} {
		t.Run(field, func(t *testing.T) {
			dir, _ := updateRelease(t)
			path := filepath.Join(dir, "manifest.json")
			data, _ := os.ReadFile(path)
			var manifest map[string]any
			json.Unmarshal(data, &manifest)
			delete(manifest, field)
			data, _ = json.Marshal(manifest)
			os.WriteFile(path, data, 0600)
			if _, _, err := (&Handler{releaseDir: dir}).updateArtifact("linux-amd64"); err == nil {
				t.Fatalf("accepted missing %s", field)
			}
		})
	}
}

func TestAgentUpdateAuthorizationPolicyPackagesAndHealth(t *testing.T) {
	pool, sessions := agentTestService(t)
	ctx := context.Background()
	admin, err := sessions.CreateFirstUser(ctx, "update-admin", "Admin", "correct horse battery staple", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := createTestBackupAgent(ctx, t, pool)
	if err != nil {
		t.Fatal(err)
	}
	var deviceID string
	if err = pool.QueryRow(ctx, `SELECT device_id::text FROM agent_identities WHERE id=$1`, identity.agentID).Scan(&deviceID); err != nil {
		t.Fatal(err)
	}
	dir, expected := updateRelease(t)
	h := NewHandler(pool, sessions)
	h.ConfigureDownloads("https://vigara.example", dir)
	mux := http.NewServeMux()
	h.Register(mux)
	call := func(method, path, body, cookie, bearer string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookie})
		}
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	settings := "/api/devices/" + deviceID + "/agent-update"
	for _, path := range []string{"/api/agent/update-plan", "/api/agent/update-package/" + expected.SHA256, "/api/agent/update-health"} {
		if w := call("GET", path, "", "", ""); w.Code != 401 {
			t.Fatalf("unauthorized %s: %d", path, w.Code)
		}
	}
	if w := call("POST", settings, `{"updateNow":true}`, "", identity.credential); w.Code != 401 {
		t.Fatalf("agent changed operator policy: %d", w.Code)
	}
	if w := call("GET", "/api/agent/update-plan", "", "", identity.credential); w.Code != 204 {
		t.Fatalf("automatic updates must default off: %d %s", w.Code, w.Body.String())
	}
	w := call("POST", settings, `{"updateNow":true}`, admin.Token, "")
	if w.Code != 200 {
		t.Fatalf("request update: %d %s", w.Code, w.Body.String())
	}
	w = call("GET", "/api/agent/update-plan", "", "", identity.credential)
	var plan protocol.AgentUpdatePlan
	if json.Unmarshal(w.Body.Bytes(), &plan) != nil || w.Code != 200 || plan.SHA256 != expected.SHA256 || plan.Generation != 1 {
		t.Fatalf("plan %d %s", w.Code, w.Body.String())
	}
	w = call("GET", "/api/agent/update-package/"+expected.SHA256, "", "", identity.credential)
	if w.Code != 200 || int64(w.Body.Len()) != expected.Size {
		t.Fatalf("package %d", w.Code)
	}
	if w = call("GET", "/api/agent/update-package/"+strings.Repeat("0", 64), "", "", identity.credential); w.Code != 409 {
		t.Fatalf("served wrong immutable package: %d", w.Code)
	}
	if w = call("POST", settings, `{"automatic":true}`, admin.Token, ""); w.Code != 200 {
		t.Fatalf("enable automatic: %d %s", w.Code, w.Body.String())
	}
	if w = call("POST", "/api/agent/update-status", `{"state":"rolled_back","version":"new","message":"Restored previous version"}`, "", identity.credential); w.Code != 204 {
		t.Fatalf("report %d %s", w.Code, w.Body.String())
	}
	w = call("GET", settings, "", admin.Token, "")
	if !strings.Contains(w.Body.String(), `"rolled_back"`) || !strings.Contains(w.Body.String(), `"automatic":true`) {
		t.Fatalf("settings %s", w.Body.String())
	}
	if w = call("POST", settings, `{"updateNow":true}`, admin.Token, ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = call("GET", "/api/agent/update-plan", "", "", identity.credential)
	json.Unmarshal(w.Body.Bytes(), &plan)
	if plan.Generation != 2 {
		t.Fatalf("explicit retry generation %d", plan.Generation)
	}
	// Health uses a server timestamp, so client clock skew cannot approve stale polls.
	w = call("GET", "/api/agent/update-health", "", "", identity.credential)
	var baseline struct {
		LastSeen time.Time `json:"lastSeenAt"`
	}
	if json.Unmarshal(w.Body.Bytes(), &baseline) != nil || baseline.LastSeen.IsZero() {
		t.Fatalf("baseline %s", w.Body.String())
	}
	healthPath := "/api/agent/update-health?version=new&since=" + baseline.LastSeen.Format(time.RFC3339Nano)
	w = call("GET", healthPath, "", "", identity.credential)
	if !strings.Contains(w.Body.String(), `"healthy":false`) {
		t.Fatalf("stale health %s", w.Body.String())
	}
	if _, err = pool.Exec(ctx, `UPDATE agent_identities SET agent_version='new' WHERE id=$1`, identity.agentID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE devices SET last_seen_at=$2 WHERE id=$1`, deviceID, baseline.LastSeen.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	w = call("GET", healthPath, "", "", identity.credential)
	if !strings.Contains(w.Body.String(), `"healthy":true`) {
		t.Fatalf("fresh health %s", w.Body.String())
	}
	if _, err = pool.Exec(ctx, `UPDATE users SET role='viewer' WHERE id=$1`, admin.User.ID); err != nil {
		t.Fatal(err)
	}
	if w = call("POST", settings, `{"updateNow":true}`, admin.Token, ""); w.Code != 403 {
		t.Fatalf("viewer changed update policy: %d", w.Code)
	}
	if w = call("GET", settings, "", admin.Token, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"canManage":false`) {
		t.Fatalf("viewer settings %d %s", w.Code, w.Body.String())
	}
	if _, err = pool.Exec(ctx, `UPDATE agent_identities SET revoked_at=now() WHERE id=$1`, identity.agentID); err != nil {
		t.Fatal(err)
	}
	if w = call("GET", "/api/agent/update-plan", "", "", identity.credential); w.Code != 401 {
		t.Fatalf("revoked agent got plan: %d", w.Code)
	}
}
