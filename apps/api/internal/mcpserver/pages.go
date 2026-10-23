package mcpserver

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/audit"
)

const csrfCookie = "vigara_mcp_csrf"

func (h *Handler) csrfSignature(value, target string) string {
	mac := hmac.New(sha256.New, h.csrfKey)
	_, _ = mac.Write([]byte(value + "\n" + target))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (h *Handler) newCSRF(w http.ResponseWriter, r *http.Request) string {
	value := secret() + "." + strconv.FormatInt(time.Now().Add(10*time.Minute).Unix(), 10)
	token := value + "." + h.csrfSignature(value, r.URL.RequestURI())
	http.SetCookie(w, &http.Cookie{Name: csrfCookie, Value: token, Path: "/oauth", HttpOnly: true, Secure: h.issuer[:5] == "https", SameSite: http.SameSiteLaxMode, MaxAge: 600})
	return token
}
func (h *Handler) clearCSRF(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: csrfCookie, Path: "/oauth", HttpOnly: true, Secure: h.issuer[:5] == "https", SameSite: http.SameSiteLaxMode, MaxAge: -1})
}
func (h *Handler) checkCSRF(w http.ResponseWriter, r *http.Request) bool {
	cookie, err := r.Cookie(csrfCookie)
	if origin := r.Header.Get("Origin"); origin != "" && origin != h.issuer {
		http.Error(w, "request origin rejected", 403)
		return false
	}
	// The nonce and expiration are signed together with the exact form target.
	parts := strings.Split(r.PostForm.Get("csrf"), ".")
	valid := false
	if err == nil && len(parts) == 3 && len(parts[0]) == 43 {
		expires, _ := strconv.ParseInt(parts[1], 10, 64)
		signature := h.csrfSignature(parts[0]+"."+parts[1], r.URL.RequestURI())
		valid = expires > time.Now().Unix() && subtle.ConstantTimeCompare([]byte(parts[2]), []byte(signature)) == 1 && subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(r.PostForm.Get("csrf"))) == 1
	}
	if !valid {
		http.Error(w, "Form expired. Reload this page and try again.", 403)
		return false
	}
	return true
}

var page = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>{{.Title}} · Vigara</title>
<style>html{color-scheme:dark;font:16px/1.6 system-ui,sans-serif;background:#10141c;color:#e8edf5}body{margin:0;padding:32px 20px}main{max-width:640px;margin:5vh auto;background:#192130;border:1px solid #344056;border-radius:16px;padding:32px}h1{font-size:1.8rem;line-height:1.2}h2{font-size:1.15rem}a{color:#8cc6ff}code{overflow-wrap:anywhere}label{display:block}input:not([type=hidden]){display:block;box-sizing:border-box;width:100%;margin-top:6px;padding:12px;border:1px solid #52617b;border-radius:6px;background:#10141c;color:inherit;font:inherit}button{padding:10px 18px;margin:8px 8px 8px 0;border:1px solid #668bb7;border-radius:6px;background:#254b73;color:white;font:inherit;cursor:pointer}button:focus-visible,input:focus-visible,a:focus-visible{outline:3px solid #8cc6ff;outline-offset:3px}article{border-top:1px solid #344056;padding:16px 0}[role=alert]{color:#ffb9b9}@media(max-width:500px){main{padding:20px;margin:0 auto}body{padding:16px}}</style></head>
<body><main><h1>{{.Title}}</h1>
{{if .Consent}}
<p><strong>{{.Client}}</strong> is requesting access to this Vigara instance.</p>
<p>Sign in with your local Vigara account to approve this connection. The client name is supplied by the application; check the callback address before approving.</p>
<p>Return address: <code>{{.Redirect}}</code></p>
<p>This connection can read the following information for up to 30 days:</p><ul>{{range .Scopes}}<li>{{.}}</li>{{end}}</ul>
<p>You can disconnect it at <a href="/oauth/connections">Connected applications</a>. Your account permissions still apply.</p>
{{if .Error}}<p role="alert">{{.Error}}</p>{{end}}
<form method="post"><input type="hidden" name="csrf" value="{{.CSRF}}">
<p><label>Username <input name="username" autocomplete="username" maxlength="64" required></label></p>
<p><label>Password <input name="password" type="password" autocomplete="current-password" maxlength="1024" required></label></p>
<button name="decision" value="allow">Allow access</button><button name="decision" value="deny" formnovalidate>Deny</button></form>
{{else}}
{{if .Login}}<p><a href="/">Sign in to Vigara</a>, then return to this page to manage connected applications.</p>{{else}}
<p>Connected applications for {{.Username}}. Disconnecting immediately invalidates access and refresh tokens.</p>
{{range .Connections}}<article><h2>{{.Name}}</h2><p>Approved {{.Created}} · Expires {{.Expires}}</p><p>Scopes: {{range .Scopes}}{{.}} {{end}}</p>
<form method="post"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="id" value="{{.ID}}"><button>Disconnect</button></form></article>
{{else}}<p>No connected applications.</p>{{end}}{{end}}
{{end}}<p><a href="/">Back to Vigara</a></p></main></body></html>`))

func renderPage(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	_ = page.Execute(w, data)
}

func (h *Handler) consentPage(w http.ResponseWriter, r *http.Request, a authorization, message string) {
	labels := map[string]string{"devices.read": "Device names, platforms, and monitoring status", "containers.read": "Container names, images, and observed state", "backups.read": "Backup results and status messages", "alerts.read": "Alert incidents and recent failures"}
	scopes := []string{}
	for _, scope := range a.Scopes {
		scopes = append(scopes, labels[scope])
	}
	renderPage(w, map[string]any{"Title": "Connect to Vigara", "Consent": true, "Client": a.Client.Name, "Redirect": a.Redirect, "Scopes": scopes, "CSRF": h.newCSRF(w, r), "Error": message})
}

type connection struct {
	ID, Name         string
	Scopes           []string
	Created, Expires time.Time
}

func (h *Handler) connections(w http.ResponseWriter, r *http.Request) {
	user, ok := h.accounts.UserFromRequest(r)
	if !ok {
		if r.Method == http.MethodPost {
			http.Error(w, "login required", 401)
			return
		}
		renderPage(w, map[string]any{"Title": "Connected applications", "Login": true})
		return
	}
	if r.Method == http.MethodPost {
		if !parseForm(w, r) || !h.checkCSRF(w, r) {
			return
		}
		id := r.PostForm.Get("id")
		// Compare text instead of casting user input to a UUID.
		tag, err := h.pool.Exec(r.Context(), `DELETE FROM user_sessions s USING mcp_oauth_grants g WHERE g.session_id=s.id AND s.user_id=$1::uuid AND g.id::text=$2`, user.ID, id)
		if err != nil {
			serverError(w)
			return
		}
		if tag.RowsAffected() > 0 {
			h.record(r.Context(), user, "mcp.revoke", id, audit.OutcomeSuccess)
		}
		h.clearCSRF(w)
		http.Redirect(w, r, "/oauth/connections", http.StatusSeeOther)
		return
	}
	rows, err := h.pool.Query(r.Context(), `SELECT g.id::text,c.name,g.scopes,g.created_at,g.expires_at FROM mcp_oauth_grants g
 JOIN user_sessions s ON s.id=g.session_id JOIN mcp_oauth_clients c ON c.id=g.client_id WHERE s.user_id=$1::uuid AND s.expires_at>now() AND g.expires_at>now() ORDER BY g.created_at DESC LIMIT 512`, user.ID)
	if err != nil {
		serverError(w)
		return
	}
	defer rows.Close()
	connections := []connection{}
	for rows.Next() {
		var c connection
		if rows.Scan(&c.ID, &c.Name, &c.Scopes, &c.Created, &c.Expires) != nil {
			serverError(w)
			return
		}
		connections = append(connections, c)
	}
	if rows.Err() != nil {
		serverError(w)
		return
	}
	renderPage(w, map[string]any{"Title": "Connected applications", "Username": user.Username, "Connections": connections, "CSRF": h.newCSRF(w, r)})
}
