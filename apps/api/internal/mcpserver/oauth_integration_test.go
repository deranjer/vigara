package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/apps/api/internal/db"
	"github.com/deranjer/vigara/apps/api/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const testPassword = "correct horse battery staple"

type fixture struct {
	h        *Handler
	mux      *http.ServeMux
	accounts *auth.Service
	session  auth.Session
	pool     *pgxpool.Pool
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	raw := os.Getenv("VIGIRA_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set VIGIRA_TEST_DATABASE_URL for PostgreSQL integration tests")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("mcp_test_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
		if err != nil {
			t.Error(err)
		}
		_ = admin.Close(context.Background())
	})
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	if err = migrations.Apply(u.String()); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	accounts := auth.NewService(pool, db.New(pool))
	session, err := accounts.CreateFirstUser(ctx, "admin", "Administrator", testPassword, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(pool, accounts, nil, "https://vigara.example")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.Register(mux)
	return fixture{h, mux, accounts, session, pool}
}

func (f fixture) register(t *testing.T) client {
	t.Helper()
	w := request(f.mux, "POST", "/oauth/register", `{"client_name":"Test client","redirect_uris":["https://client.example/callback?kept=yes"],"token_endpoint_auth_method":"none"}`, map[string]string{"Content-Type": "application/json"})
	if w.Code != 201 {
		t.Fatalf("register: %d %s", w.Code, w.Body)
	}
	var c client
	if json.Unmarshal(w.Body.Bytes(), &c) != nil {
		t.Fatal("invalid registration response")
	}
	return c
}

func (f fixture) code(t *testing.T, c client, scopes string) (string, string) {
	t.Helper()
	verifier := secret()
	challenge := base64.RawURLEncoding.EncodeToString(hash(verifier))
	q := url.Values{"response_type": {"code"}, "client_id": {c.ID}, "redirect_uri": {c.RedirectURIs[0]}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}, "resource": {"https://vigara.example/mcp"}, "state": {"unchanged-state"}}
	if scopes != "" {
		q.Set("scope", scopes)
	}
	path := "/oauth/authorize?" + q.Encode()
	w := request(f.mux, "GET", path, "", nil)
	if w.Code != 200 {
		t.Fatalf("consent: %d %s", w.Code, w.Body)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing CSRF cookie")
	}
	form := url.Values{"csrf": {cookies[0].Value}, "decision": {"allow"}, "username": {"admin"}, "password": {testPassword}}
	w = request(f.mux, "POST", path, form.Encode(), map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Origin": "https://vigara.example"}, cookies[0])
	if w.Code != 303 {
		t.Fatalf("authorize: %d %s", w.Code, w.Body)
	}
	location, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if location.Host != "client.example" || location.Query().Get("state") != "unchanged-state" || location.Query().Get("kept") != "yes" || location.Query().Get("iss") != "https://vigara.example" {
		t.Fatalf("invalid callback: %s", location)
	}
	return location.Query().Get("code"), verifier
}

type tokens struct {
	Access  string `json:"access_token"`
	Refresh string `json:"refresh_token"`
	Scope   string `json:"scope"`
}

func (f fixture) exchange(c client, code, verifier string) *httptest.ResponseRecorder {
	return request(f.mux, "POST", "/oauth/token", url.Values{"grant_type": {"authorization_code"}, "client_id": {c.ID}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {c.RedirectURIs[0]}, "resource": {"https://vigara.example/mcp"}}.Encode(), map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
}
func (f fixture) refresh(c client, token, scope string) *httptest.ResponseRecorder {
	return request(f.mux, "POST", "/oauth/token", url.Values{"grant_type": {"refresh_token"}, "client_id": {c.ID}, "refresh_token": {token}, "scope": {scope}}.Encode(), map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
}
func mustTokens(t *testing.T, w *httptest.ResponseRecorder) tokens {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("token response: %d %s", w.Code, w.Body)
	}
	var out tokens
	if json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.Access) != 43 || len(out.Refresh) != 43 {
		t.Fatal("invalid tokens")
	}
	return out
}

func TestOAuthLifecycleAndMonitoring(t *testing.T) {
	f := newFixture(t)
	c := f.register(t)
	code, verifier := f.code(t, c, "")
	issued := mustTokens(t, f.exchange(c, code, verifier))
	if w := f.exchange(c, code, verifier); w.Code != 400 {
		t.Fatal("authorization code replay succeeded")
	}
	// Restart the handler with the same database: registered clients, access and
	// refresh tokens must persist, while pending browser forms may expire.
	restarted, err := New(f.pool, f.accounts, nil, "https://vigara.example")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	restarted.Register(mux)
	f.h = restarted
	f.mux = mux
	w := rpc(f.mux, issued.Access, "initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "test", "version": "1"}})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"serverInfo"`) {
		t.Fatalf("initialize: %d %s", w.Code, w.Body)
	}
	ctx := context.Background()
	var deviceID string
	if err = f.pool.QueryRow(ctx, `INSERT INTO devices(display_name) VALUES('Test NAS') RETURNING id::text`).Scan(&deviceID); err != nil {
		t.Fatal(err)
	}
	var agentID string
	if err = f.pool.QueryRow(ctx, `INSERT INTO agent_identities(device_id,credential_hash,protocol_version) VALUES($1::uuid,$2,'1') RETURNING id::text`, deviceID, hash(secret())).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `INSERT INTO agent_telemetry_latest(agent_id,report_id,observed_at,received_at,payload)
	VALUES($1::uuid,'test',now(),now(),'{"dockerContainers":[{"id":"container-1","name":"web","image":"nginx","state":"running","env":["SECRET=hidden"]}],"errors":[]}'::jsonb)`, agentID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `INSERT INTO backup_status(device_id,policy_key,status,observed_at,message,source_id) VALUES($1::uuid,'daily','failure',now(),'Test backup failure','test')`, deviceID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `WITH rule AS (INSERT INTO alert_rules(name,kind,config) VALUES('Test failure','backup_failure','{}') RETURNING id)
 INSERT INTO alert_incidents(rule_id,status,summary,started_at) SELECT id,'firing','Test backup failure',now() FROM rule`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"list_devices", "list_containers", "list_backup_status", "list_alerts"} {
		w = rpc(f.mux, issued.Access, "tools/call", map[string]any{"name": name, "arguments": map[string]int{"limit": 1}})
		if w.Code != 200 || strings.Contains(w.Body.String(), `"isError":true`) || !strings.Contains(w.Body.String(), `"structuredContent"`) {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), "hidden") || (name == "list_containers" && !strings.Contains(w.Body.String(), "container-1")) {
			t.Fatalf("container projection is missing or leaks configuration: %s", w.Body)
		}
	}
	w = rpc(f.mux, issued.Access, "tools/call", map[string]any{"name": "list_devices", "arguments": map[string]int{"limit": 101}})
	if !strings.Contains(w.Body.String(), `"isError":true`) {
		t.Fatal("page bound not enforced")
	}
	next := mustTokens(t, f.refresh(c, issued.Refresh, "devices.read"))
	if next.Scope != "devices.read" {
		t.Fatalf("scopes not narrowed: %s", next.Scope)
	}
	if w = rpc(f.mux, issued.Access, "tools/list", map[string]any{}); w.Code != 401 {
		t.Fatal("old access token accepted after rotation")
	}
	w = rpc(f.mux, next.Access, "tools/call", map[string]any{"name": "list_alerts", "arguments": map[string]any{}})
	if !strings.Contains(w.Body.String(), `"isError":true`) {
		t.Fatal("narrowed scope bypass")
	}
	if w = f.refresh(c, next.Refresh, "devices.read alerts.read"); w.Code != 400 {
		t.Fatal("refresh expanded consent")
	}
	// Reusing a consumed refresh token invalidates the entire connection.
	if w = f.refresh(c, issued.Refresh, ""); w.Code != 400 {
		t.Fatal("refresh replay succeeded")
	}
	if w = rpc(f.mux, next.Access, "tools/list", map[string]any{}); w.Code != 401 {
		t.Fatal("replay failed to revoke connection")
	}
}

func TestConnectionPageOwnershipAndConcurrentRefresh(t *testing.T) {
	f := newFixture(t)
	c := f.register(t)
	code, verifier := f.code(t, c, "")
	issued := mustTokens(t, f.exchange(c, code, verifier))
	p, err := f.h.authenticate(context.Background(), issued.Access)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.accounts.CreateUser(context.Background(), "viewer", "Viewer", testPassword, auth.RoleViewer); err != nil {
		t.Fatal(err)
	}
	viewer, err := f.accounts.Login(context.Background(), "viewer", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	for _, session := range []auth.Session{viewer, f.session} {
		webCookie := &http.Cookie{Name: auth.SessionCookieName, Value: session.Token}
		w := request(f.mux, "GET", "/oauth/connections", "", nil, webCookie)
		if w.Code != 200 {
			t.Fatalf("connections page: %d %s", w.Code, w.Body)
		}
		csrf := w.Result().Cookies()[0]
		w = request(f.mux, "POST", "/oauth/connections", url.Values{"id": {p.GrantID}, "csrf": {csrf.Value}}.Encode(), map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Origin": "https://vigara.example"}, csrf, webCookie)
		if w.Code != 303 {
			t.Fatalf("disconnect: %d %s", w.Code, w.Body)
		}
		w = rpc(f.mux, issued.Access, "tools/list", map[string]any{})
		if session.User.ID == viewer.User.ID && w.Code != 200 {
			t.Fatal("viewer revoked another account's connection")
		}
		if session.User.ID == f.session.User.ID && w.Code != 401 {
			t.Fatal("owner could not revoke connection")
		}
	}
	code, verifier = f.code(t, c, "")
	issued = mustTokens(t, f.exchange(c, code, verifier))
	var wg sync.WaitGroup
	responses := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); responses <- f.refresh(c, issued.Refresh, "") }()
	}
	wg.Wait()
	close(responses)
	statuses := []int{}
	var next tokens
	for response := range responses {
		statuses = append(statuses, response.Code)
		if response.Code == 200 {
			next = mustTokens(t, response)
		}
	}
	slices.Sort(statuses)
	if !slices.Equal(statuses, []int{200, 400}) {
		t.Fatalf("concurrent refresh: %v", statuses)
	}
	if w := rpc(f.mux, next.Access, "tools/list", map[string]any{}); w.Code != 401 {
		t.Fatal("concurrent replay left a valid connection")
	}
}

func TestAccessExpiryAndRoleDemotion(t *testing.T) {
	f := newFixture(t)
	c := f.register(t)
	code, verifier := f.code(t, c, "")
	issued := mustTokens(t, f.exchange(c, code, verifier))
	if _, err := f.pool.Exec(context.Background(), `UPDATE mcp_oauth_grants SET access_expires_at=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if w := rpc(f.mux, issued.Access, "tools/list", map[string]any{}); w.Code != 401 {
		t.Fatal("expired access token accepted")
	}
	next := mustTokens(t, f.refresh(c, issued.Refresh, ""))
	// Preserve another administrator so demoting this account is allowed.
	if _, err := f.accounts.CreateUser(context.Background(), "another", "Other admin", testPassword, auth.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if _, err := f.accounts.SetUserRole(context.Background(), f.session.User.ID, auth.RoleViewer); err != nil {
		t.Fatal(err)
	}
	if w := rpc(f.mux, next.Access, "tools/list", map[string]any{}); w.Code != 401 {
		t.Fatal("connection survived role demotion")
	}
	if w := f.refresh(c, next.Refresh, ""); w.Code != 400 {
		t.Fatal("refresh survived role demotion")
	}
}

func TestOAuthClientPKCEExpiryAndAccountBinding(t *testing.T) {
	f := newFixture(t)
	c := f.register(t)
	other := f.register(t)
	code, verifier := f.code(t, c, "devices.read")
	if w := f.exchange(other, code, verifier); w.Code != 400 {
		t.Fatal("cross-client code exchange")
	}
	if w := f.exchange(c, code, secret()); w.Code != 400 {
		t.Fatal("invalid verifier accepted")
	}
	if w := f.exchange(c, code, verifier); w.Code != 400 {
		t.Fatal("code survived failed PKCE")
	}
	code, verifier = f.code(t, c, "devices.read")
	if _, err := f.pool.Exec(context.Background(), `UPDATE mcp_oauth_codes SET expires_at=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if w := f.exchange(c, code, verifier); w.Code != 400 {
		t.Fatal("expired code accepted")
	}
	code, verifier = f.code(t, c, "devices.read")
	issued := mustTokens(t, f.exchange(c, code, verifier))
	if w := f.refresh(other, issued.Refresh, ""); w.Code != 400 {
		t.Fatal("cross-client refresh accepted")
	}
	// Password reset revokes OAuth through the dedicated account session FK.
	if _, err := f.accounts.ChangePassword(context.Background(), f.session.Token, testPassword, "replacement password 12345", "replacement password 12345"); err != nil {
		t.Fatal(err)
	}
	if w := rpc(f.mux, issued.Access, "tools/list", map[string]any{}); w.Code != 401 {
		t.Fatal("access survived password change")
	}
	if w := f.refresh(c, issued.Refresh, ""); w.Code != 400 {
		t.Fatal("refresh survived password change")
	}
}

func TestConcurrentCodeExchangeAndRevocationOwnership(t *testing.T) {
	f := newFixture(t)
	c := f.register(t)
	other := f.register(t)
	code, verifier := f.code(t, c, "")
	var wg sync.WaitGroup
	responses := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); responses <- f.exchange(c, code, verifier) }()
	}
	wg.Wait()
	close(responses)
	var issued tokens
	statuses := []int{}
	for response := range responses {
		statuses = append(statuses, response.Code)
		if response.Code == 200 {
			issued = mustTokens(t, response)
		}
	}
	slices.Sort(statuses)
	if !slices.Equal(statuses, []int{200, 400}) {
		t.Fatalf("concurrent exchange: %v", statuses)
	}
	revoke := func(clientID string) *httptest.ResponseRecorder {
		return request(f.mux, "POST", "/oauth/revoke", url.Values{"client_id": {clientID}, "token": {issued.Refresh}}.Encode(), map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	}
	if revoke(other.ID).Code != 200 {
		t.Fatal("wrong-client revocation response")
	}
	if w := rpc(f.mux, issued.Access, "tools/list", map[string]any{}); w.Code != 200 {
		t.Fatal("another client revoked connection")
	}
	if revoke(c.ID).Code != 200 {
		t.Fatal("revocation failed")
	}
	if w := rpc(f.mux, issued.Access, "tools/list", map[string]any{}); w.Code != 401 {
		t.Fatal("access survived revoke")
	}
	if w := f.refresh(c, issued.Refresh, ""); w.Code != 400 {
		t.Fatal("refresh survived revoke")
	}
}
