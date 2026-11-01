package mcpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/apps/api/internal/containers"
	"github.com/deranjer/vigara/apps/api/internal/stacks"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func managementTransport(h *Handler, p principal) http.Handler {
	server := h.tools(p)
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
}

func TestManagementScopeAndRoleBoundary(t *testing.T) {
	for _, op := range managementOperations {
		for _, tc := range []struct {
			name, role string
			scopes     []string
			allowed    bool
		}{
			{"no consent", auth.RoleAdmin, nil, false},
			{"old read consent", auth.RoleAdmin, []string{"devices.read", "containers.read", "backups.read", "alerts.read"}, op.scope == "containers.read"},
			{"viewer", auth.RoleViewer, supportedScopes, op.scope == "containers.read"},
			{"operator", auth.RoleOperator, supportedScopes, op.scope != "stacks.deploy"},
			{"administrator", auth.RoleAdmin, []string{op.scope}, true},
		} {
			t.Run(op.name+"/"+tc.name, func(t *testing.T) {
				h, _ := testHandler(t)
				called := false
				h.management = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					called = true
					p, ok := r.Context().Value(principalKey{}).(principal)
					if !ok || p.User.ID != "owner" || r.Header.Get("Cookie") != "" {
						t.Fatal("lost identity or forwarded credentials")
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"accepted":true}`))
				})
				p := principal{User: auth.User{ID: "owner", Role: tc.role}, Scopes: tc.scopes}
				w := rpc(managementTransport(h, p), "", "tools/call", map[string]any{"name": op.name, "arguments": managementInput{StackID: "web", AgentID: "00000000-0000-0000-0000-000000000001", ContainerID: "web", JobID: "job", Kind: "images", Body: map[string]any{"action": "start"}}})
				if called != tc.allowed || strings.Contains(w.Body.String(), `"isError":true`) == tc.allowed {
					t.Fatalf("allowed=%v called=%v: %s", tc.allowed, called, w.Body)
				}
			})
		}
	}
}

func TestManagementCatalogAndBackupBoundary(t *testing.T) {
	h, _ := testHandler(t)
	h.ConfigureManagement(stacks.Config{}, containers.Config{})
	handler := managementTransport(h, principal{User: auth.User{Role: auth.RoleAdmin}, Scopes: supportedScopes})
	w := rpc(handler, "", "tools/list", map[string]any{})
	var listing struct {
		Result struct {
			Tools []mcp.Tool `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if len(listing.Result.Tools) != len(managementOperations)+6 {
		t.Fatalf("missing tools: %s", w.Body)
	}
	for _, tool := range listing.Result.Tools {
		if strings.Contains(tool.Name, "backup") && (tool.Annotations == nil || !tool.Annotations.ReadOnlyHint) {
			t.Fatalf("backup write exposed: %s", tool.Name)
		}
		for _, op := range managementOperations {
			if tool.Name == op.name && (tool.Annotations == nil || tool.Annotations.ReadOnlyHint != op.readOnly) {
				t.Fatalf("wrong annotations: %s", tool.Name)
			}
		}
	}
	for _, scope := range supportedScopes {
		if strings.HasPrefix(scope, "backups.") && scope != "backups.read" {
			t.Fatalf("backup write scope: %s", scope)
		}
	}
	for _, name := range []string{"delete_backup", "update_backup_settings", "exec", "http_request"} {
		w := rpc(handler, "", "tools/call", map[string]any{"name": name, "arguments": map[string]any{}})
		if !strings.Contains(w.Body.String(), `"error"`) && !strings.Contains(w.Body.String(), `"isError":true`) {
			t.Fatalf("unexpected exposed operation: %s", w.Body)
		}
	}
	// Exercise actual feature handlers: malformed writes are refused before DB
	// access; lifecycle calls cannot be routed into a backup handler.
	w = rpc(handler, "", "tools/call", map[string]any{"name": "update_stack_draft", "arguments": managementInput{StackID: "web", Body: map[string]any{"files": map[string]string{"../backup": "x"}}}})
	if !strings.Contains(w.Body.String(), "HTTP 422") {
		t.Fatalf("draft validation bypass: %s", w.Body)
	}
}

func TestManagementRejectsPathInjection(t *testing.T) {
	for _, op := range managementOperations {
		for _, bad := range []string{"..", ".", "../backups/policies", "a%2fb", "a?reveal=env", "a/b", "a#fragment", ""} {
			input := managementInput{StackID: bad, AgentID: bad, ContainerID: bad, JobID: bad, Kind: bad}
			if strings.Contains(op.path, "{") {
				if target, err := op.target(input); err == nil {
					t.Fatalf("%s accepted %q: %s", op.name, bad, target)
				}
			}
		}
	}
}

func TestManagementAuthorizerRejectsCookieAndMissingPrincipal(t *testing.T) {
	r := httptest.NewRequest("GET", "https://vigara.example/api/stacks", nil)
	r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "web-session"})
	if _, ok := (managementAuthorizer{}).Authorize(httptest.NewRecorder(), r, auth.PermStacksDeploy); ok {
		t.Fatal("cookie authenticated private API")
	}
	if slices.Contains(supportedScopes, "containers.exec") {
		t.Fatal("raw exec would bypass backup boundary")
	}
}

func TestManagementStreamFailureAndCompletion(t *testing.T) {
	for _, tc := range []struct {
		body      string
		wantError bool
	}{
		{"{\"step\":\"done\"}\n", false},
		{"{\"step\":\"error\",\"message\":\"pull failed\"}\n", true},
		{"{\"step\":\"pulling\"}\n", true},
	} {
		h, _ := testHandler(t)
		h.management = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/x-ndjson")
			_, _ = w.Write([]byte(tc.body))
		})
		w := rpc(managementTransport(h, principal{User: auth.User{Role: auth.RoleOperator}, Scopes: []string{"containers.manage"}}), "", "tools/call", map[string]any{"name": "pull_docker_image", "arguments": managementInput{AgentID: "00000000-0000-0000-0000-000000000001", Body: map[string]any{"ref": "nginx:alpine"}}})
		if strings.Contains(w.Body.String(), `"isError":true`) != tc.wantError {
			t.Fatalf("%s: %s", tc.body, w.Body)
		}
	}
}

func TestConsentDescribesWriteAccessAndReadOnlyBackups(t *testing.T) {
	h, _ := testHandler(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "https://vigara.example/oauth/authorize", nil)
	h.consentPage(w, r, authorization{Client: client{Name: "Desktop client"}, Redirect: "http://127.0.0.1:3456/callback", Scopes: supportedScopes}, "")
	for _, text := range []string{"Start, stop, restart and remove containers", "create and edit stacks", "Backup access is read-only", "127.0.0.1:3456/callback"} {
		if !strings.Contains(w.Body.String(), text) {
			t.Fatalf("consent missing %q: %s", text, w.Body)
		}
	}
	if strings.Contains(w.Body.String(), "can read the following information") {
		t.Fatal("write consent misrepresented as read only")
	}
}
