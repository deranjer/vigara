package mcpserver

import (
	"net/http"
	"strings"
)

// StatusHandler reports configured connection details only to signed-in users.
// The URL comes from operator configuration, never request or proxy headers.
func StatusHandler(accounts Accounts, issuer string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := accounts.UserFromRequest(r); !ok {
			http.Error(w, "login required", http.StatusUnauthorized)
			return
		}
		serverURL := ""
		if issuer != "" {
			serverURL = strings.TrimSuffix(issuer, "/") + "/mcp"
		}
		jsonResponse(w, http.StatusOK, struct {
			Enabled   bool   `json:"enabled"`
			ServerURL string `json:"serverUrl,omitempty"`
		}{Enabled: issuer != "", ServerURL: serverURL})
	}
}
