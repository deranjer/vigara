package containers

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/internal/protocol"
)

func filePost(t *testing.T, f *fixture, agent, operation, role, body, origin string) *http.Response {
	t.Helper()
	r, _ := http.NewRequest(http.MethodPost, f.server.URL+"/api/docker/"+agent+"/files/"+operation, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if role != "" {
		r.AddCookie(&http.Cookie{Name: "role", Value: role})
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestMountFilesRequireAdminAndSameOrigin(t *testing.T) {
	f := newFixture(t)
	body := `{"kind":"mount","id":"caddy","mount":"/etc/caddy","path":"Caddyfile"}`
	for _, role := range []string{"", auth.RoleViewer, auth.RoleOperator} {
		for _, op := range []string{"list", "read", "write"} {
			resp := filePost(t, f, agentExec, op, role, body, "")
			if resp.StatusCode != 401 && resp.StatusCode != 403 {
				t.Fatalf("%s %s = %d", role, op, resp.StatusCode)
			}
		}
	}
	if resp := filePost(t, f, agentExec, "read", auth.RoleAdmin, body, "https://attacker.example"); resp.StatusCode != 403 {
		t.Fatalf("cross-origin request = %d", resp.StatusCode)
	}
}

func TestMountFilesEnforceHostTierAndValidateRequests(t *testing.T) {
	f := newFixture(t)
	f.connectAgent(agentManage)
	body := `{"kind":"volume","id":"data"}`
	if resp := filePost(t, f, agentManage, "list", auth.RoleAdmin, body, ""); resp.StatusCode != 409 {
		t.Fatalf("manage tier = %d", resp.StatusCode)
	}
	for _, bad := range []string{
		`{"kind":"host","id":"/etc"}`, `{"kind":"volume","id":"../etc"}`, `{"kind":"mount","id":"caddy"}`,
		`{"kind":"volume","id":"data","source":"/etc"}`, `{"kind":"volume","id":"data"} {}`,
		`{"kind":"volume","id":"data","content":"unexpected"}`,
	} {
		if resp := filePost(t, f, agentExec, "read", auth.RoleAdmin, bad, ""); resp.StatusCode != 400 {
			t.Fatalf("bad body = %d: %s", resp.StatusCode, bad)
		}
	}
}

func TestMountFileReadWriteRelayAndAuditExcludeContents(t *testing.T) {
	f := newFixture(t)
	agent := f.connectAgent(agentExec)
	agent.serve(func(call protocol.ChannelFrame) protocol.ChannelFrame {
		var p protocol.DockerFileParams
		if err := json.Unmarshal(call.Params, &p); err != nil {
			t.Error(err)
		}
		if p.Kind != "mount" || p.Mount != "/etc/caddy" || p.Path != "Caddyfile" {
			t.Errorf("unexpected params: %+v", p)
		}
		if call.Method == protocol.MethodDockerFilesWrite {
			if p.Content != "secret-file-content" || p.Version != "old-version" {
				t.Error("write params not relayed")
			}
			return protocol.ChannelFrame{Error: &protocol.ChannelError{Code: protocol.ErrCodeConflict, Message: "file changed"}}
		}
		return protocol.ChannelFrame{Result: json.RawMessage(`{"content":"secret-file-content","version":"old-version","writable":true}`)}
	})
	body := `{"kind":"mount","id":"caddy","mount":"/etc/caddy","path":"Caddyfile"}`
	resp := filePost(t, f, agentExec, "read", auth.RoleAdmin, body, "")
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(data), "secret-file-content") || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("read = %d %s", resp.StatusCode, data)
	}
	body = `{"kind":"mount","id":"caddy","mount":"/etc/caddy","path":"Caddyfile","content":"secret-file-content","version":"old-version"}`
	resp = filePost(t, f, agentExec, "write", auth.RoleAdmin, body, "")
	if resp.StatusCode != 409 {
		t.Fatalf("conflict = %d", resp.StatusCode)
	}
	f.audit.mu.Lock()
	defer f.audit.mu.Unlock()
	if len(f.audit.events) != 2 {
		t.Fatalf("audit = %+v", f.audit.events)
	}
	for _, event := range f.audit.events {
		encoded, _ := json.Marshal(event)
		for _, sensitive := range []string{"secret-file-content", "Caddyfile", "old-version"} {
			if strings.Contains(string(encoded), sensitive) {
				t.Fatalf("audit exposed %s", sensitive)
			}
		}
	}
}
