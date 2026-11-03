package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/deranjer/vigara/apps/api/internal/audit"
	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/internal/composesecrets"
	"github.com/deranjer/vigara/internal/protocol"
	"github.com/deranjer/vigara/internal/stack"
)

type environmentAgent struct {
	fakeAgent
	key        string
	offline    bool
	recipients int
}

func (f *environmentAgent) Call(ctx context.Context, agent, method string, in, out any) error {
	if method == protocol.MethodComposeRecipient {
		f.recipients++
		if f.offline {
			return errors.New("offline")
		}
		*out.(*protocol.ComposeRecipient) = protocol.ComposeRecipient{Recipient: f.key}
		return nil
	}
	return f.fakeAgent.Call(ctx, agent, method, in, out)
}

func TestNewStackEnvironmentProvisionsOnceAndReusesRecovery(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("protected environment reveal requires Linux")
	}
	pool := testPool(t)
	ctx := context.Background()
	repo, _ := importRepository(t)
	var user, device, agent string
	if err := pool.QueryRow(ctx, "INSERT INTO users(username,display_name,password_hash) VALUES('env-owner','Owner','test') RETURNING id::text").Scan(&user); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "INSERT INTO devices(display_name) VALUES('Docker') RETURNING id::text").Scan(&device); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "INSERT INTO agent_identities(device_id,credential_hash,protocol_version) VALUES($1,decode('abcdef','hex'),'v1') RETURNING id::text", device).Scan(&agent); err != nil {
		t.Fatal(err)
	}
	agentFile := filepath.Join(t.TempDir(), "agent.age")
	key, err := composesecrets.EnsureAgeIdentity(agentFile)
	if err != nil {
		t.Fatal(err)
	}
	hub := &environmentAgent{key: key}
	mux := http.NewServeMux()
	api := Register(mux, Config{Pool: pool, Repository: repo, Hub: hub, Audit: audit.NewRecorder(pool), Authz: authorizer{auth.User{ID: user, Username: "env-owner", Role: auth.RoleAdmin}}})
	// Simulate an already-downloaded key from an earlier import.
	_, recovery, err := api.importKeys()
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path string, body any) *httptest.ResponseRecorder {
		data, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(string(data))))
		return w
	}
	for _, id := range []string{"plex", "next-stack"} {
		if w := call("POST", "/api/stacks", map[string]string{"id": id, "name": id, "agentId": agent, "projectName": id}); w.Code != 201 {
			t.Fatal(w.Code, w.Body)
		}
		var raw []byte
		if err = pool.QueryRow(ctx, "SELECT files FROM stack_drafts WHERE stack_id=$1", id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		s := stack.Snapshot{ID: id}
		if err = json.Unmarshal(raw, &s.Files); err != nil {
			t.Fatal(err)
		}
		value := "TIMEZONE=America/New_York\nADVERTISE_IP=https://plex.example\nPLEX_KEY=synthetic-env-test-secret\n"
		q := environmentRequest{Path: "secrets/plex.env.sops", Source: environmentSource{Version: 1, Digest: stack.Digest(s)}, Value: &value}
		if id == "plex" {
			hub.offline = true
			w := call("POST", "/api/stacks/"+id+"/environment", q)
			if w.Code != 422 || strings.Contains(w.Body.String(), "synthetic-env-test-secret") {
				t.Fatal("offline setup should explain required agent", w.Code, w.Body)
			}
			hub.offline = false
			hub.key = "invalid-recipient"
			w = call("POST", "/api/stacks/"+id+"/environment", q)
			if w.Code != 422 {
				t.Fatal("invalid agent public key accepted", w.Code)
			}
			hub.key = key
			binding, _ := api.lookupTarget(ctx, id)
			if len(binding.Recipients) != 0 {
				t.Fatal("failed setup persisted recipients")
			}
			hub.recipients = 0
		}
		w := call("POST", "/api/stacks/"+id+"/environment", q)
		if w.Code != 200 || strings.Contains(w.Body.String(), "synthetic-env-test-secret") {
			t.Fatal("first encryption", w.Code, w.Body)
		}
		var encrypted struct{ Path, Ciphertext, Manifest string }
		if err = json.Unmarshal(w.Body.Bytes(), &encrypted); err != nil {
			t.Fatal(err)
		}
		if encrypted.Path != "secrets/plex.env.age" || !composesecrets.IsAgeArtifact([]byte(encrypted.Ciphertext)) {
			t.Fatal("legacy request did not return native age", encrypted.Path)
		}
		binding, ok := api.lookupTarget(ctx, id)
		if !ok || !automaticEnvironmentPolicy(binding) {
			t.Fatal("policy not persisted", binding)
		}
		for _, record := range binding.Recipients {
			if record.Class == composesecrets.OperatorRecovery && record.Recipient != recovery {
				t.Fatal("recovery key replaced")
			}
		}
		for _, file := range []string{api.CentralAgeIdentity, agentFile, filepath.Join(importKeyDirectory(api.Config), "recovery.age")} {
			plain, err := composesecrets.DecryptDotenv(ctx, "", file, []byte(encrypted.Ciphertext))
			if err != nil || string(plain) != value {
				t.Fatal("recipient cannot decrypt", err)
			}
		}
		// No connected device is needed after setup, including a repeated legacy request.
		hub.offline = true
		before := hub.recipients
		w = call("POST", "/api/stacks/"+id+"/environment", q)
		if w.Code != 200 || hub.recipients != before {
			t.Fatal("offline retry failed", w.Code, w.Body)
		}
		if err = json.Unmarshal(w.Body.Bytes(), &encrypted); err != nil || encrypted.Path != "secrets/plex.env.age" {
			t.Fatal("retry changed format", err)
		}
		s.Files[encrypted.Path] = encrypted.Ciphertext
		s.Files["vigara-stack.yaml"] = encrypted.Manifest
		w = call("PUT", "/api/stacks/"+id+"/draft", draft{Files: s.Files, Version: 1})
		if w.Code != 200 {
			t.Fatal("ciphertext draft failed", w.Code, w.Body)
		}
		w = call("POST", "/api/stacks/"+id+"/environment/reveal", environmentRequest{Path: encrypted.Path, Source: environmentSource{Version: 2, Digest: stack.Digest(s)}})
		if w.Code != 200 || !strings.Contains(w.Body.String(), "synthetic-env-test-secret") {
			t.Fatal("offline reveal failed", w.Code, w.Body)
		}
		if w = call("POST", "/api/stacks/"+id+"/environment", q); w.Code != 409 {
			t.Fatal("stale draft encrypted", w.Code)
		}
		hub.offline = false
		if w = call("POST", "/api/stacks/"+id+"/save", map[string]int{"version": 2}); w.Code != 200 {
			t.Fatal("save failed", w.Code, w.Body)
		}
		saved, err := repo.Read(ctx, id, "")
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(saved.Files)
		if strings.Contains(string(encoded), "synthetic-env-test-secret") || strings.Contains(string(encoded), "AGE-SECRET-KEY") {
			t.Fatal("secret entered Git")
		}
	}
	if hub.recipients != 2 || len(hub.deployed) != 0 {
		t.Fatal("setup repeated or deployed", hub.recipients)
	}
	var details string
	if err = pool.QueryRow(ctx, "SELECT coalesce(string_agg(detail::text,','),'') FROM audit_events").Scan(&details); err != nil || strings.Contains(details, "synthetic-env-test-secret") {
		t.Fatal("audit leaked plaintext", err)
	}
}
