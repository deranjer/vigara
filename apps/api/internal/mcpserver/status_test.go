package mcpserver

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/deranjer/vigara/apps/api/internal/auth"
)

type statusAccounts struct {
	Accounts
	signedIn bool
}

func (a statusAccounts) UserFromRequest(*http.Request) (auth.User, bool) {
	return auth.User{Role: auth.RoleViewer}, a.signedIn
}

func TestConnectionStatus(t *testing.T) {
	for _, tc := range []struct {
		name, issuer, wantURL string
		signedIn              bool
		wantStatus            int
	}{
		{"anonymous", "https://vigara.example", "", false, 401},
		{"disabled", "", "", true, 200},
		{"configured", "https://vigara.example/", "https://vigara.example/mcp", true, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := StatusHandler(statusAccounts{signedIn: tc.signedIn}, tc.issuer)
			w := request(h, "GET", "/api/mcp/status", "", map[string]string{"X-Forwarded-Host": "evil.example"})
			if w.Code != tc.wantStatus {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			if w.Code != http.StatusOK {
				return
			}
			var body struct {
				Enabled   bool   `json:"enabled"`
				ServerURL string `json:"serverUrl"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Enabled != (tc.issuer != "") || body.ServerURL != tc.wantURL || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("unexpected connection status: %s", w.Body)
			}
		})
	}
}
