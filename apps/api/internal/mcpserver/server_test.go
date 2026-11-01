package mcpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestURLValidation(t *testing.T) {
	for _, raw := range []string{"https://vigara.example", "https://vigara.example:8443/", "http://localhost:8080", "http://127.0.0.1:8080", "http://[::1]:8080"} {
		if err := ValidateURL(raw); err != nil {
			t.Errorf("%q: %v", raw, err)
		}
	}
	for _, raw := range []string{"", "http://vigara.example", "http://192.168.1.10", "https://user:pass@vigara.example", "https://vigara.example/mcp", "https://vigara.example?x=1", "https://vigara.example#fragment", "//vigara.example"} {
		if ValidateURL(raw) == nil {
			t.Errorf("accepted unsafe issuer %q", raw)
		}
	}
	for _, raw := range []string{"https://client.example/callback?mode=connect", "http://localhost:3456/callback"} {
		if !validRedirect(raw) {
			t.Errorf("rejected callback %q", raw)
		}
	}
	for _, raw := range []string{"javascript:alert(1)", "https://client.example/callback#fragment", "http://client.example/callback", "https://user@client.example/callback", "//client.example/callback"} {
		if validRedirect(raw) {
			t.Errorf("accepted unsafe callback %q", raw)
		}
	}
}

func request(handler http.Handler, method, path, body string, headers map[string]string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://vigara.example"+path, strings.NewReader(body))
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	for _, cookie := range cookies {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func testHandler(t *testing.T) (*Handler, *http.ServeMux) {
	t.Helper()
	h, err := New(nil, nil, nil, "https://vigara.example")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.Register(mux)
	return h, mux
}

func TestDiscoveryAndAuthenticationBoundary(t *testing.T) {
	_, mux := testHandler(t)
	for _, path := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-authorization-server"} {
		w := request(mux, "GET", path, "", nil)
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || !json.Valid(w.Body.Bytes()) {
			t.Fatalf("discovery %s: %d %s", path, w.Code, w.Body)
		}
	}
	for _, headers := range []map[string]string{nil, {"Authorization": "Bearer invalid"}, {"Cookie": auth.SessionCookieName + "=" + secret()}} {
		w := request(mux, "POST", "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, headers)
		if w.Code != 401 || !strings.Contains(w.Header().Get("WWW-Authenticate"), "resource_metadata=") {
			t.Fatalf("MCP bypass: %d %s", w.Code, w.Body)
		}
	}
	w := request(mux, "POST", "/mcp", "", map[string]string{"Origin": "https://evil.example"})
	if w.Code != 403 {
		t.Fatalf("cross-origin request accepted: %d", w.Code)
	}
	r := httptest.NewRequest("POST", "https://evil.example/mcp", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("unconfigured host accepted")
	}
}

func TestCSRFBoundToCookieOriginTargetAndSignature(t *testing.T) {
	h, _ := testHandler(t)
	target := "/oauth/authorize?client_id=client-a"
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "https://vigara.example"+target, nil)
	csrf := h.newCSRF(w, r)
	cookie := w.Result().Cookies()[0]
	for _, tc := range []struct {
		name, path, token, origin string
		cookie                    *http.Cookie
		want                      bool
	}{
		{"valid", target, csrf, "https://vigara.example", cookie, true},
		{"cookie missing", target, csrf, "", nil, false},
		{"form missing", target, "", "", cookie, false},
		{"cross origin", target, csrf, "https://evil.example", cookie, false},
		{"opaque origin", target, csrf, "null", cookie, false},
		{"changed client", "/oauth/authorize?client_id=client-b", csrf, "", cookie, false},
		{"forged cookie", target, "forged", "", &http.Cookie{Name: csrfCookie, Value: "forged"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "https://vigara.example"+tc.path, strings.NewReader(url.Values{"csrf": {tc.token}}.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Origin", tc.origin)
			if tc.cookie != nil {
				req.AddCookie(tc.cookie)
			}
			response := httptest.NewRecorder()
			if !parseForm(response, req) {
				t.Fatal("parse form")
			}
			if got := h.checkCSRF(response, req); got != tc.want {
				t.Errorf("CSRF accepted=%v", got)
			}
		})
	}
}

func TestOAuthPagesPreserveOriginForFormSubmission(t *testing.T) {
	h, _ := testHandler(t)
	r := httptest.NewRequest("GET", "https://vigara.example/oauth/authorize?client_id=client-a", nil)
	w := httptest.NewRecorder()
	h.consentPage(w, r, authorization{Client: client{Name: "MCP client"}, Redirect: "https://client.example/callback", Scopes: supportedScopes}, "")
	if got := w.Header().Get("Referrer-Policy"); got != "same-origin" {
		t.Fatalf("consent referrer policy %q can suppress the Origin on a form POST", got)
	}
	if !strings.Contains(w.Body.String(), `<form method="post">`) {
		t.Fatal("missing same-origin consent form")
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "form-action 'self'") {
		t.Fatal("form target must remain same origin")
	}
	// Connection revocation uses the same renderer and needs the same policy.
	w = httptest.NewRecorder()
	renderPage(w, map[string]any{"Title": "Connected applications"})
	if got := w.Header().Get("Referrer-Policy"); got != "same-origin" {
		t.Fatalf("connections referrer policy = %q", got)
	}
}

func TestConsentPolicyAllowsOnlyRegisteredCallbackOrigin(t *testing.T) {
	h, _ := testHandler(t)
	for _, callback := range []string{
		"https://chatgpt.com/connector_platform_oauth_redirect",
		"https://client.example:8443/callback?state=example",
		"http://127.0.0.1:3456/callback",
		"http://[::1]:3456/callback",
	} {
		t.Run(callback, func(t *testing.T) {
			r := httptest.NewRequest("GET", "https://vigara.example/oauth/authorize", nil)
			w := httptest.NewRecorder()
			h.consentPage(w, r, authorization{Redirect: callback}, "")
			u, _ := url.Parse(callback)
			want := "default-src 'none'; style-src 'unsafe-inline'; form-action 'self' " + u.Scheme + "://" + u.Host + "; frame-ancestors 'none'; base-uri 'none'"
			if got := w.Header().Get("Content-Security-Policy"); got != want {
				t.Fatalf("callback redirect blocked or policy broadened:\ngot %s\nwant %s", got, want)
			}
			if w.Header().Get("Referrer-Policy") != "same-origin" {
				t.Fatal("lost origin-preserving referrer policy")
			}
		})
	}
	// Other OAuth pages do not redirect a form to an external client.
	w := httptest.NewRecorder()
	renderPage(w, map[string]any{"Title": "Connected applications"})
	if got := w.Header().Get("Content-Security-Policy"); !strings.Contains(got, "form-action 'self';") {
		t.Fatalf("connections form policy: %s", got)
	}
}

func TestConsentPolicyRejectsSourceAndDirectiveInjection(t *testing.T) {
	for _, callback := range []string{
		"", "javascript:alert(1)", "https://*.example/callback", "https://client.example;script-src/callback",
		"https://client.example,evil.example/callback", "https://user:pass@client.example/callback", "http://client.example/callback",
		"https://client.example/\r\nContent-Security-Policy:default-src *",
	} {
		if got := consentFormAction(callback); got != "'self'" {
			t.Errorf("unsafe callback %q produced %q", callback, got)
		}
	}
	// Callback paths and queries may legally contain CSP-looking text. None of
	// it belongs in a source expression; only the validated origin is emitted.
	if got := consentFormAction("https://client.example/callback;script-src?next=https://evil.example&policy=*"); got != "'self' https://client.example" {
		t.Fatalf("callback path/query leaked into CSP: %s", got)
	}
}

func TestRejectMalformedOAuthRequestsBeforeDatabase(t *testing.T) {
	_, mux := testHandler(t)
	for _, tc := range []struct{ path, body, content string }{
		{"/oauth/register", `{"redirect_uris":["http://evil.example/callback"]}`, "application/json"},
		{"/oauth/register", `{"redirect_uris":["https://client.example/callback"],"token_endpoint_auth_method":"client_secret_post"}`, "application/json"},
		{"/oauth/register", strings.Repeat("x", 20<<10), "application/json"},
		{"/oauth/token", "client_id=a&client_id=b&grant_type=refresh_token", "application/x-www-form-urlencoded"},
		{"/oauth/token", "client_id=a&grant_type=client_credentials", "application/x-www-form-urlencoded"},
		{"/oauth/token", "client_id=a&grant_type=refresh_token&resource=https://evil.example/mcp", "application/x-www-form-urlencoded"},
		{"/oauth/token", "client_id=a&grant_type=authorization_code&code=x&code_verifier=x", "application/x-www-form-urlencoded"},
	} {
		w := request(mux, "POST", tc.path, tc.body, map[string]string{"Content-Type": tc.content})
		if w.Code != 400 {
			t.Fatalf("%s %q: %d %s", tc.path, tc.body[:min(len(tc.body), 100)], w.Code, w.Body)
		}
	}
}

func rpc(handler http.Handler, token, method string, params any) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	headers := map[string]string{"Content-Type": "application/json", "Accept": "application/json, text/event-stream", "MCP-Protocol-Version": "2025-11-25"}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	return request(handler, "POST", "/mcp", string(body), headers)
}

func TestToolSchemasProfileAndScopeEnforcement(t *testing.T) {
	h, _ := testHandler(t)
	// SDK transport with an authenticated principal, without a database. Denied
	// tool calls must stop before data access. No shared user state is captured.
	server := h.tools(principal{User: auth.User{ID: "opaque-user-id", DisplayName: "Alex", Role: auth.RoleViewer}, Scopes: []string{}})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	w := rpc(handler, "", "tools/list", map[string]any{})
	if w.Code != 200 {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	var listing struct {
		Result struct {
			Tools []mcp.Tool `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if len(listing.Result.Tools) != 6 {
		t.Fatalf("tool count %d: %s", len(listing.Result.Tools), w.Body)
	}
	for _, tool := range listing.Result.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatalf("missing read-only annotation: %s", tool.Name)
		}
	}
	w = rpc(handler, "", "tools/call", map[string]any{"name": "get_profile", "arguments": map[string]any{}})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"opaque-user-id"`) {
		t.Fatalf("profile: %d %s", w.Code, w.Body)
	}
	for _, name := range []string{"list_devices", "list_containers", "list_backup_status", "list_alerts"} {
		w = rpc(handler, "", "tools/call", map[string]any{"name": name, "arguments": map[string]any{}})
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"isError":true`) {
			t.Fatalf("scope bypass %s: %d %s", name, w.Code, w.Body)
		}
	}
}
