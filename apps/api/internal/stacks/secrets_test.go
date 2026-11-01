package stacks

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/deranjer/vigara/apps/api/internal/audit"
	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/internal/composesecrets"
	"github.com/deranjer/vigara/internal/stack"
)

func secretFixture() stack.Snapshot {
	return stack.Snapshot{ID: "demo", Files: map[string]string{
		"vigara-stack.yaml":        "apiVersion: vigara.io/v1\nkind: Stack\nstackId: demo\ndisplayName: Demo\nprojectName: demo\ncompose:\n  files: [compose.yaml]\n  projectDirectory: .\n  interpolationFiles: [{path: secrets/runtime.env.sops, purpose: stack, sensitivity: encrypted, format: sops-binary}]\nsecretRecipientRefs: [{class: central-editor, keyRef: editor}, {class: target-agent, keyRef: target}, {class: operator-recovery, keyRef: recovery}]\nconfigurationRevisionPolicy: sha256-git-tree-and-placement-inputs-v1\n",
		"compose.yaml":             "services:\n  web:\n    image: nginx\n",
		"secrets/runtime.env.sops": "ciphertext selected by source",
	}}
}

func TestEnvironmentSourceAndDeclaredPaths(t *testing.T) {
	s := secretFixture()
	if got, err := encryptedInput(s, "secrets/runtime.env.sops"); err != nil || got != s.Files["secrets/runtime.env.sops"] {
		t.Fatalf("declared input: %q %v", got, err)
	}
	s.Files["unlisted.sops"] = "not a declared input"
	for _, path := range []string{"../keys/key.sops", "/etc/key.sops", "C:/key.sops", "secrets/runtime.env", "compose.yaml", "unlisted.sops"} {
		if _, err := encryptedInput(s, path); err == nil {
			t.Fatalf("unsafe or undeclared path accepted: %s", path)
		}
	}
	for _, source := range []environmentSource{{}, {Version: -1}, {Version: 1}, {Commit: "main"}, {Commit: strings.Repeat("a", 40), Version: 1, Digest: stack.Digest(s)}, {Version: 1, Digest: "sha256:" + strings.Repeat("z", 64)}} {
		if source.valid() {
			t.Fatalf("ambiguous source accepted: %+v", source)
		}
	}
	for _, source := range []environmentSource{{Version: 1, Digest: stack.Digest(s)}, {Commit: strings.Repeat("a", 40)}} {
		if !source.valid() {
			t.Fatalf("exact source rejected: %+v", source)
		}
	}
}

func TestEnvironmentRequiresAuthorizationAndStrictRequest(t *testing.T) {
	for _, endpoint := range []string{"/environment", "/environment/reveal"} {
		mux := http.NewServeMux()
		Register(mux, Config{Authz: authorizer{auth.User{Role: auth.RoleViewer}}})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/stacks/demo"+endpoint, strings.NewReader(`{}`)))
		if w.Code != 403 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("authorization: %d %v", w.Code, w.Header())
		}
		mux = http.NewServeMux()
		Register(mux, Config{Authz: authorizer{auth.User{Role: auth.RoleAdmin}}})
		for _, body := range []string{`{"identityFile":"/etc/private"}`, `{"path":"../secret.sops","source":{"version":1}}`, `{"path":"secret.sops","source":{"commit":"main"}}`} {
			w = httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/stacks/demo"+endpoint, strings.NewReader(body)))
			if w.Code != 400 && w.Code != 422 {
				t.Fatalf("unsafe request accepted: %d", w.Code)
			}
			if strings.Contains(w.Body.String(), "/etc/private") {
				t.Fatal("private path echoed")
			}
		}
	}
}

func TestEnvironmentOfflineRevealEditAuditAndSourceFence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("protected identities require Linux service ownership/modes")
	}
	sops := os.Getenv("VIGIRA_SOPS_BINARY")
	if sops == "" {
		sops, _ = exec.LookPath("sops")
	}
	if sops == "" {
		t.Skip("install SOPS for real secret endpoint integration")
	}
	pool := testPool(t)
	ctx := context.Background()
	identities := []*age.X25519Identity{}
	policy := []composesecrets.RecipientRecord{}
	for i, class := range []composesecrets.RecipientClass{composesecrets.CentralEditor, composesecrets.TargetAgent, composesecrets.OperatorRecovery} {
		identity, err := age.GenerateX25519Identity()
		if err != nil {
			t.Fatal(err)
		}
		identities = append(identities, identity)
		policy = append(policy, composesecrets.RecipientRecord{Class: class, KeyRef: []string{"editor", "target", "recovery"}[i], Recipient: identity.Recipient().String()})
	}
	keyDir := t.TempDir()
	if err := os.Chmod(keyDir, 0700); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(keyDir, "editor.txt")
	if err := os.WriteFile(keyPath, []byte(identities[0].String()), 0600); err != nil {
		t.Fatal(err)
	}
	const original = "PASSWORD=secret-original-unique\n"
	ciphertext, err := composesecrets.EncryptForPolicy(ctx, sops, []byte(original), policy)
	if err != nil {
		t.Fatal(err)
	}
	s := secretFixture()
	s.Files["secrets/runtime.env.sops"] = string(ciphertext)
	var user string
	if err = pool.QueryRow(ctx, "INSERT INTO users(username,display_name,password_hash) VALUES('secret-editor','Secret editor','test-only') RETURNING id::text").Scan(&user); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(s.Files)
	if _, err = pool.Exec(ctx, "INSERT INTO stack_drafts(stack_id,user_id,base_commit,files) VALUES('demo',$1,'',$2)", user, raw); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	// No hub or target connection is provided: reveal/edit must remain offline.
	api := Register(mux, Config{Pool: pool, SOPS: sops, CentralAgeIdentity: keyPath, Audit: audit.NewRecorder(pool), Authz: authorizer{auth.User{ID: user, Username: "secret-editor", Role: auth.RoleAdmin}}, Targets: map[string]Binding{"demo": {Recipients: policy}}})
	call := func(endpoint string, q environmentRequest) *httptest.ResponseRecorder {
		body, _ := json.Marshal(q)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/stacks/demo/environment"+endpoint, strings.NewReader(string(body))))
		return w
	}
	q := environmentRequest{Path: "secrets/runtime.env.sops", Source: environmentSource{Version: 1, Digest: stack.Digest(s)}}
	w := call("/reveal", q)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "secret-original-unique") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("reveal: %d %s", w.Code, w.Body)
	}
	const replacement = "PASSWORD=secret-replacement-unique\n"
	value := replacement
	q.Value = &value
	w = call("", q)
	if w.Code != 200 || strings.Contains(w.Body.String(), "secret-replacement-unique") {
		t.Fatalf("edit: %d %s", w.Code, w.Body)
	}
	var result struct {
		Ciphertext string `json:"ciphertext"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	plain, err := composesecrets.DecryptDotenv(ctx, sops, keyPath, []byte(result.Ciphertext))
	if err != nil || string(plain) != replacement {
		t.Fatalf("roundtrip: %v", err)
	}
	var details string
	if err = pool.QueryRow(ctx, "SELECT string_agg(action || outcome || detail::text, ',') FROM audit_events WHERE target_id='demo'").Scan(&details); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(details, "stack.environment.revealsuccess") || !strings.Contains(details, "stack.environment.editsuccess") || strings.Contains(details, "secret-") || strings.Contains(details, keyPath) {
		t.Fatalf("unsafe/incomplete audit: %s", details)
	}
	// Same draft version with changed ciphertext (including delete/recreate) must fail.
	q.Value = nil
	s.Files[q.Path] = "changed ciphertext"
	raw, _ = json.Marshal(s.Files)
	if _, err = pool.Exec(ctx, "UPDATE stack_drafts SET files=$1 WHERE stack_id='demo'", raw); err != nil {
		t.Fatal(err)
	}
	if w = call("/reveal", q); w.Code != 409 {
		t.Fatalf("stale ciphertext accepted: %d", w.Code)
	}
	q.Source.Digest = stack.Digest(s)
	// Restore valid ciphertext so missing/unavailable auditing is the only barrier.
	s.Files[q.Path] = string(ciphertext)
	raw, _ = json.Marshal(s.Files)
	if _, err = pool.Exec(ctx, "UPDATE stack_drafts SET files=$1 WHERE stack_id='demo'", raw); err != nil {
		t.Fatal(err)
	}
	q.Source.Digest = stack.Digest(s)
	if _, err = pool.Exec(ctx, `CREATE FUNCTION reject_secret_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit unavailable'; END $$; CREATE TRIGGER reject_secret_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_secret_audit()`); err != nil {
		t.Fatal(err)
	}
	if w = call("/reveal", q); w.Code != 503 || strings.Contains(w.Body.String(), "secret-original-unique") {
		t.Fatalf("audit failure returned plaintext: %d", w.Code)
	}
	api.Audit = nil
	if w = call("/reveal", q); w.Code != 503 || strings.Contains(w.Body.String(), original) {
		t.Fatalf("unaudited reveal: %d", w.Code)
	}
}
