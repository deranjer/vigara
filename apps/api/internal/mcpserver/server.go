// Package mcpserver exposes Vigara monitoring through authenticated MCP.
package mcpserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/audit"
	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var supportedScopes = []string{"devices.read", "containers.read", "backups.read", "alerts.read"}

type Accounts interface {
	Login(context.Context, string, string) (auth.Session, error)
	Logout(context.Context, string) error
	UserFromRequest(*http.Request) (auth.User, bool)
}

type Handler struct {
	pool     *pgxpool.Pool
	accounts Accounts
	audit    *audit.Recorder
	issuer   string
	mcp      http.Handler
	csrfKey  []byte
}

// ValidateURL requires a stable HTTPS origin. Loopback HTTP is supported only
// for local development. Forwarding and Host headers never determine issuer.
func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("MCP URL must be an HTTPS origin without a path, query, credentials, or fragment")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && slices.Contains([]string{"localhost", "127.0.0.1", "::1"}, u.Hostname())) {
		return errors.New("MCP URL requires HTTPS (HTTP is allowed only on loopback for development)")
	}
	return nil
}

func New(pool *pgxpool.Pool, accounts Accounts, recorder *audit.Recorder, issuer string) (*Handler, error) {
	if err := ValidateURL(issuer); err != nil {
		return nil, err
	}
	h := &Handler{pool: pool, accounts: accounts, audit: recorder, issuer: strings.TrimSuffix(issuer, "/"), csrfKey: randomBytes()}
	h.mcp = mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return h.tools(r.Context().Value(principalKey{}).(principal))
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, DisableLocalhostProtection: true})
	return h, nil
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", h.resourceMetadata)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", h.resourceMetadata)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", h.authorizationMetadata)
	mux.HandleFunc("POST /oauth/register", h.registerClient)
	mux.HandleFunc("GET /oauth/authorize", h.authorize)
	mux.HandleFunc("POST /oauth/authorize", h.authorize)
	mux.HandleFunc("POST /oauth/token", h.token)
	mux.HandleFunc("POST /oauth/revoke", h.revoke)
	mux.HandleFunc("GET /oauth/connections", h.connections)
	mux.HandleFunc("POST /oauth/connections", h.connections)
	mux.Handle("/mcp", http.HandlerFunc(h.serveMCP))
}

func (h *Handler) resourceMetadata(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, 200, map[string]any{"resource": h.issuer + "/mcp", "authorization_servers": []string{h.issuer}, "scopes_supported": supportedScopes, "bearer_methods_supported": []string{"header"}, "resource_name": "Vigara"})
}
func (h *Handler) authorizationMetadata(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, 200, map[string]any{
		"issuer": h.issuer, "authorization_endpoint": h.issuer + "/oauth/authorize", "token_endpoint": h.issuer + "/oauth/token",
		"registration_endpoint": h.issuer + "/oauth/register", "revocation_endpoint": h.issuer + "/oauth/revoke",
		"response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code", "refresh_token"},
		"token_endpoint_auth_methods_supported": []string{"none"}, "revocation_endpoint_auth_methods_supported": []string{"none"},
		"code_challenge_methods_supported": []string{"S256"}, "scopes_supported": supportedScopes,
		"authorization_response_iss_parameter_supported": true,
	})
}

func jsonResponse(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
func oauthError(w http.ResponseWriter, status int, code, message string) {
	jsonResponse(w, status, map[string]string{"error": code, "error_description": message})
}
func secret() string { return base64.RawURLEncoding.EncodeToString(randomBytes()) }
func randomBytes() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}
func hash(s string) []byte { v := sha256.Sum256([]byte(s)); return v[:] }
func decodeJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	d := json.NewDecoder(r.Body)
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		oauthError(w, 400, "invalid_request", "Invalid JSON request")
		return false
	}
	return true
}
func parseForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if r.ParseForm() != nil {
		oauthError(w, 400, "invalid_request", "Invalid form request")
		return false
	}
	for _, values := range r.PostForm {
		if len(values) != 1 {
			oauthError(w, 400, "invalid_request", "Duplicate form parameter")
			return false
		}
	}
	return true
}

type principalKey struct{}
type principal struct {
	User    auth.User
	Scopes  []string
	GrantID string
}

func (h *Handler) authenticate(ctx context.Context, token string) (principal, error) {
	var p principal
	if len(token) != 43 {
		return p, pgx.ErrNoRows
	}
	err := h.pool.QueryRow(ctx, `SELECT u.id::text,u.username,u.display_name,u.role,g.scopes,g.id::text
 FROM mcp_oauth_grants g JOIN user_sessions s ON s.id=g.session_id JOIN users u ON u.id=s.user_id
 WHERE g.access_hash=$1 AND g.resource=$2 AND g.access_expires_at>now() AND g.expires_at>now() AND s.expires_at>now()`, hash(token), h.issuer+"/mcp").Scan(
		&p.User.ID, &p.User.Username, &p.User.DisplayName, &p.User.Role, &p.Scopes, &p.GrantID)
	return p, err
}

func (h *Handler) serveMCP(w http.ResponseWriter, r *http.Request) {
	// The configured external host replaces the SDK loopback heuristic, which
	// would reject legitimate HTTPS proxies forwarding through loopback.
	issuer, _ := url.Parse(h.issuer)
	if !strings.EqualFold(r.Host, issuer.Host) {
		http.Error(w, "request host rejected", http.StatusForbidden)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != h.issuer {
		http.Error(w, "request origin rejected", 403)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	fields := strings.Fields(r.Header.Get("Authorization"))
	var p principal
	var err error = pgx.ErrNoRows
	if len(fields) == 2 && strings.EqualFold(fields[0], "Bearer") {
		p, err = h.authenticate(r.Context(), fields[1])
	}
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			oauthError(w, 503, "temporarily_unavailable", "Could not validate connection")
			return
		}
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata=%q, error="invalid_token"`, h.issuer+"/.well-known/oauth-protected-resource"))
		oauthError(w, 401, "invalid_token", "Connect your Vigara account")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	ctx, cancel := context.WithTimeout(context.WithValue(r.Context(), principalKey{}, p), 30*time.Second)
	defer cancel()
	h.mcp.ServeHTTP(w, r.WithContext(ctx))
}

func (h *Handler) record(ctx context.Context, user auth.User, action, target, outcome string) {
	_ = h.audit.Record(ctx, audit.Event{ActorID: user.ID, ActorName: user.Username, Action: action, TargetKind: "mcp_connection", TargetID: target, Outcome: outcome})
}

// RunCleanup bounds expired authorization material without expiring registered
// client identities, which clients may reuse across many reconnections.
func (h *Handler) RunCleanup(ctx context.Context) {
	cleanup := func() {
		work, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		_, _ = h.pool.Exec(work, `DELETE FROM user_sessions WHERE id IN
		(SELECT session_id FROM mcp_oauth_codes WHERE expires_at<=now()
		UNION SELECT session_id FROM mcp_oauth_grants WHERE expires_at<=now())`)
	}
	cleanup()
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanup()
		}
	}
}
