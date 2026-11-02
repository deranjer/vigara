package stacks

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/apps/api/internal/githubapp"
)

type githubTransport func(*http.Request) (*http.Response, error)

func (f githubTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGitHubPublicURLValidation(t *testing.T) {
	if base, err := githubPublicURL("https://vigara.example.com/"); err != nil || base != "https://vigara.example.com" {
		t.Fatal(base, err)
	}
	for _, raw := range []string{"", "http://vigara.example.com", "https://user:password@vigara.example.com", "https://vigara.example.com/path", "https://vigara.example.com?redirect=evil", "https://vigara.example.com#fragment"} {
		if _, err := githubPublicURL(raw); err == nil {
			t.Fatal("invalid public origin", raw)
		}
	}
}

func TestGitHubCallbackContinuationPreservesStrictLoginCookies(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux, Config{Authz: authorizer{auth.User{Role: auth.RoleAdmin}}})
	for _, path := range []string{githubCallback, githubInstalled} {
		r := httptest.NewRequest("GET", path+"?state=setup-state&code=manifest-code&installation_id=123&redirect=https://evil.example", nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "window.location.replace") || !strings.Contains(w.Body.String(), "continue=1") || strings.Contains(w.Body.String(), "evil.example") || w.Header().Get("Referrer-Policy") != "no-referrer" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'nonce-") {
			t.Fatal("unsafe continuation", w.Code, w.Body)
		}
		w = httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path+"?continue=1&state=setup-state", nil))
		if w.Code != 401 {
			t.Fatal("missing session looped or bypassed authentication", w.Code)
		}
	}
}

func TestGitHubRegistrationRequiresAdministratorSessionAndConfiguration(t *testing.T) {
	for _, role := range []string{auth.RoleViewer, auth.RoleOperator} {
		mux := http.NewServeMux()
		Register(mux, Config{Authz: authorizer{auth.User{Role: role}}})
		for _, path := range []string{"/api/stacks/settings/github/start", "/api/stacks/settings/github/install", "/api/stacks/settings/github/callback", "/api/stacks/settings/github/installed"} {
			method := "POST"
			if strings.HasSuffix(path, "callback") || strings.HasSuffix(path, "installed") {
				method = "GET"
			}
			w := httptest.NewRecorder()
			r := httptest.NewRequest(method, path, nil)
			r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "role-test-session"})
			mux.ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatal(role, path, w.Code)
			}
		}
	}
	mux := http.NewServeMux()
	Register(mux, Config{Authz: authorizer{auth.User{Role: auth.RoleAdmin}}})
	r := httptest.NewRequest("POST", "/api/stacks/settings/github/start", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("missing browser session accepted", w.Code)
	}
	r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "local-session"})
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 503 {
		t.Fatal("missing encryption config accepted", w.Code)
	}
}

func TestGitHubManifestInstallationPersistenceAndDisconnect(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	var user string
	if err := pool.QueryRow(ctx, "INSERT INTO users(username,display_name,password_hash) VALUES('github-owner','GitHub owner','test-only') RETURNING id::text").Scan(&user); err != nil {
		t.Fatal(err)
	}
	master := base64.StdEncoding.EncodeToString(make([]byte, 32))
	client, _ := githubapp.New(master)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	conversions := 0
	previous := http.DefaultTransport
	http.DefaultTransport = githubTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.github.com" {
			return previous.RoundTrip(r)
		}
		var body any
		switch r.URL.Path {
		case "/app-manifests/valid-code/conversions":
			conversions++
			body = map[string]any{"id": 42, "client_id": "client-id", "slug": "vigara-test", "pem": keyPEM, "client_secret": "discard-this-secret", "webhook_secret": "discard-this-too"}
		case "/repos/owner/repo/installation":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ey") {
				t.Error("missing app JWT")
			}
			body = map[string]any{"id": 123, "app_id": 42, "permissions": map[string]string{"contents": "write"}}
		case "/app/installations/123/access_tokens":
			body = map[string]any{"token": "ghs_test_secret", "expires_at": time.Now().Add(time.Hour)}
		default:
			t.Fatal("unexpected GitHub API request", r.URL.Path)
		}
		data, _ := json.Marshal(body)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(data))), Header: make(http.Header)}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	config := Config{Pool: pool, GitHub: client, PublicURL: "https://vigara.example.com", Authz: authorizer{auth.User{ID: user, Username: "github-owner", Role: auth.RoleAdmin}}}
	mux := http.NewServeMux()
	api := Register(mux, config)
	call := func(method, path, body, session string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "https://vigara.example.com"+path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	w := call("POST", "/api/stacks/settings/github/start", `{"remote":"https://github.com/owner/repo.git","branch":"testing","organization":"owner"}`, "session-one")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var start struct {
		Action   string         `json:"action"`
		Manifest map[string]any `json:"manifest"`
	}
	if json.Unmarshal(w.Body.Bytes(), &start) != nil {
		t.Fatal(w.Body)
	}
	action, _ := url.Parse(start.Action)
	state := action.Query().Get("state")
	if len(state) != 64 || action.Path != "/organizations/owner/settings/apps/new" || start.Manifest["public"] != false || start.Manifest["default_permissions"].(map[string]any)["contents"] != "write" {
		t.Fatal("invalid manifest", start)
	}
	if start.Manifest["setup_url"] != "https://vigara.example.com"+githubInstalled+"?state="+state {
		t.Fatal("callback wasn't pinned to configured origin")
	}
	callback := githubCallback + "?code=valid-code&state=" + state
	if w = call("GET", callback, "", "another-session"); w.Code != 400 || conversions != 0 {
		t.Fatal("different browser accepted", w.Code)
	}
	if w = call("GET", githubCallback+"?code=valid-code&state="+strings.Repeat("a", 64), "", "session-one"); w.Code != 400 || conversions != 0 {
		t.Fatal("forged state accepted")
	}
	if w = call("GET", callback, "", "session-one"); w.Code != 303 || conversions != 1 || !strings.Contains(w.Header().Get("Location"), "/apps/vigara-test/installations/new") {
		t.Fatal(w.Code, w.Body, w.Header())
	}
	if w = call("GET", callback, "", "session-one"); w.Code != 400 || conversions != 1 {
		t.Fatal("registration replay accepted")
	}
	var encrypted []byte
	if err = pool.QueryRow(ctx, "SELECT credentials FROM stack_github_app_setups WHERE state_hash=$1", digestValue(state)).Scan(&encrypted); err != nil || strings.Contains(string(encrypted), "PRIVATE KEY") {
		t.Fatal("unencrypted pending key", err)
	}
	if w = call("POST", "/api/stacks/settings/github/install", "", "session-one"); w.Code != 200 || !strings.Contains(w.Body.String(), state) {
		t.Fatal("resume failed", w.Code, w.Body)
	}
	installed := githubInstalled + "?state=" + state
	if w = call("GET", installed+"&installation_id=999", "", "session-one"); w.Code != 422 {
		t.Fatal("spoofed installation accepted", w.Code, w.Body)
	}
	if w = call("GET", installed+"&installation_id=123", "", "session-one"); w.Code != 303 {
		t.Fatal("installation failed", w.Code, w.Body)
	}
	if w = call("GET", installed+"&installation_id=123", "", "session-one"); w.Code != 400 {
		t.Fatal("installation replay accepted", w.Code)
	}
	w = call("GET", "/api/stacks/settings", "", "session-one")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"connected":true`) || !strings.Contains(w.Body.String(), `"branch":"testing"`) || strings.Contains(w.Body.String(), "PRIVATE KEY") || strings.Contains(w.Body.String(), "discard-this") || strings.Contains(w.Body.String(), "client_secret") {
		t.Fatal("unsafe settings", w.Code, w.Body)
	}
	if w = call("POST", "/api/stacks/settings/github/start", `{"remote":"https://github.com/owner/repo.git"}`, "session-one"); w.Code != 409 {
		t.Fatal("overwrote existing App")
	}
	_, err = pool.Exec(ctx, "INSERT INTO stack_git_settings(singleton,remote,branch,auth_mode) VALUES(true,'https://github.com/owner/repo.git','main','github-app')")
	if err != nil {
		t.Fatal(err)
	}
	// A fresh API/client reads encrypted credentials and the chosen auth mode.
	config.GitHub, _ = githubapp.New(master)
	restarted := Register(http.NewServeMux(), config)
	repository, err := restarted.repository(ctx)
	if err != nil || repository.Authenticate == nil {
		t.Fatal("auth mode did not survive restart", err)
	}
	if _, err = repository.Authenticate(ctx, repository.Remote); err != nil {
		t.Fatal("credentials did not survive restart", err)
	}
	if _, err = repository.Authenticate(ctx, "https://evil.example/owner/repo.git"); err == nil {
		t.Fatal("credentials leaked to another host")
	}
	if w = call("DELETE", "/api/stacks/settings/github", "", "session-one"); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	s, err := api.settings(ctx)
	if err != nil || s.AuthMode != "server" || s.Version != 2 {
		t.Fatal("disconnect did not invalidate settings", s, err)
	}
	if _, err = api.githubAuth(ctx, s.Remote); err == nil {
		t.Fatal("disconnected App still authenticated")
	}
	// Expired state cannot exchange a manifest code.
	w = call("POST", "/api/stacks/settings/github/start", `{"remote":"https://github.com/owner/repo.git"}`, "session-one")
	_ = json.Unmarshal(w.Body.Bytes(), &start)
	action, _ = url.Parse(start.Action)
	state = action.Query().Get("state")
	_, err = pool.Exec(ctx, "UPDATE stack_github_app_setups SET expires_at=now()-interval '1 second'")
	if err != nil {
		t.Fatal(err)
	}
	if w = call("GET", githubCallback+"?code=valid-code&state="+state, "", "session-one"); w.Code != 400 || conversions != 1 {
		t.Fatal("expired setup accepted", w.Code)
	}
}
