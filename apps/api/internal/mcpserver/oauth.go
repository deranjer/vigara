package mcpserver

import (
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/audit"
	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/jackc/pgx/v5"
)

type client struct {
	ID            string   `json:"client_id"`
	Name          string   `json:"client_name"`
	RedirectURIs  []string `json:"redirect_uris"`
	AuthMethod    string   `json:"token_endpoint_auth_method"`
	GrantTypes    []string `json:"grant_types,omitempty"`
	ResponseTypes []string `json:"response_types,omitempty"`
}

func validRedirect(raw string) bool {
	if len(raw) > 2048 {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && u.Host != "" && u.User == nil && u.Fragment == "" &&
		(u.Scheme == "https" || (u.Scheme == "http" && slices.Contains([]string{"localhost", "127.0.0.1", "::1"}, u.Hostname())))
}

// Public clients use PKCE rather than client secrets. DCR makes a self-hosted
// instance usable by either host without manually provisioning client IDs.
func (h *Handler) registerClient(w http.ResponseWriter, r *http.Request) {
	var c client
	if !decodeJSON(w, r, &c) {
		return
	}
	if len(c.RedirectURIs) < 1 || len(c.RedirectURIs) > 8 || len(c.Name) > 120 || (c.AuthMethod != "" && c.AuthMethod != "none") {
		oauthError(w, 400, "invalid_client_metadata", "Provide 1–8 redirect URIs and use token_endpoint_auth_method none")
		return
	}
	for _, uri := range c.RedirectURIs {
		if !validRedirect(uri) {
			oauthError(w, 400, "invalid_redirect_uri", "Redirect URIs must use HTTPS or loopback HTTP without credentials or fragments")
			return
		}
	}
	for _, grant := range c.GrantTypes {
		if grant != "authorization_code" && grant != "refresh_token" {
			oauthError(w, 400, "invalid_client_metadata", "Unsupported grant type")
			return
		}
	}
	for _, response := range c.ResponseTypes {
		if response != "code" {
			oauthError(w, 400, "invalid_client_metadata", "Only authorization code responses are supported")
			return
		}
	}
	if c.Name == "" {
		c.Name = "MCP client"
	}
	c.ID = secret()
	c.AuthMethod = "none"
	c.GrantTypes = []string{"authorization_code", "refresh_token"}
	c.ResponseTypes = []string{"code"}
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		serverError(w)
		return
	}
	defer tx.Rollback(r.Context())
	// Serialize the quota check across processes; unauthenticated registration
	// must not be an unbounded persistent-storage sink.
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(818741902)`); err != nil {
		serverError(w)
		return
	}
	var count int
	if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM mcp_oauth_clients`).Scan(&count); err != nil {
		serverError(w)
		return
	}
	if count >= 512 {
		oauthError(w, 429, "temporarily_unavailable", "Client registration quota reached; ask the operator to remove unused clients")
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO mcp_oauth_clients(id,name,redirect_uris) VALUES($1,$2,$3)`, c.ID, c.Name, c.RedirectURIs); err != nil {
		serverError(w)
		return
	}
	if tx.Commit(r.Context()) != nil {
		serverError(w)
		return
	}
	jsonResponse(w, 201, c)
}

type authorization struct {
	Client                               client
	Redirect, State, Challenge, Resource string
	Scopes                               []string
}

func (h *Handler) authorization(r *http.Request) (authorization, error) {
	var a authorization
	q := r.URL.Query()
	for _, v := range q {
		if len(v) != 1 {
			return a, errors.New("duplicate authorization parameter")
		}
	}
	if len(r.URL.RawQuery) > 8192 || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" {
		return a, errors.New("authorization code with S256 PKCE is required")
	}
	a.Redirect = q.Get("redirect_uri")
	a.State = q.Get("state")
	a.Challenge = q.Get("code_challenge")
	a.Resource = q.Get("resource")
	decoded, err := base64.RawURLEncoding.DecodeString(a.Challenge)
	if err != nil || len(decoded) != 32 || a.Resource != h.issuer+"/mcp" {
		return a, errors.New("invalid PKCE challenge or resource")
	}
	a.Scopes = strings.Fields(q.Get("scope"))
	if len(a.Scopes) == 0 {
		a.Scopes = slices.Clone(supportedScopes)
	}
	for _, s := range a.Scopes {
		if !slices.Contains(supportedScopes, s) {
			return a, errors.New("unsupported scope")
		}
	}
	err = h.pool.QueryRow(r.Context(), `SELECT id,name,redirect_uris FROM mcp_oauth_clients WHERE id=$1`, q.Get("client_id")).Scan(&a.Client.ID, &a.Client.Name, &a.Client.RedirectURIs)
	if err != nil || !slices.Contains(a.Client.RedirectURIs, a.Redirect) {
		return a, errors.New("unknown client or unregistered redirect URI")
	}
	return a, nil
}

func (h *Handler) authorize(w http.ResponseWriter, r *http.Request) {
	a, err := h.authorization(r)
	if err != nil {
		oauthError(w, 400, "invalid_request", err.Error())
		return
	}
	if r.Method == http.MethodGet {
		h.consentPage(w, r, a, "")
		return
	}
	if !parseForm(w, r) || !h.checkCSRF(w, r) {
		return
	}
	redirect, _ := url.Parse(a.Redirect)
	q := redirect.Query()
	q.Set("state", a.State)
	q.Set("iss", h.issuer)
	if r.PostForm.Get("decision") != "allow" {
		q.Set("error", "access_denied")
		redirect.RawQuery = q.Encode()
		http.Redirect(w, r, redirect.String(), http.StatusSeeOther)
		return
	}
	username, password := r.PostForm.Get("username"), r.PostForm.Get("password")
	if len(username) < 3 || len(username) > 64 || len(password) < 1 || len(password) > 1024 {
		h.consentPage(w, r, a, "Enter your Vigara username and password.")
		return
	}
	session, err := h.accounts.Login(r.Context(), username, password)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			h.consentPage(w, r, a, "Invalid username or password.")
		} else {
			serverError(w)
		}
		return
	}
	// Remove the dedicated login session if consent cannot be persisted.
	saved := false
	defer func() {
		if !saved {
			_ = h.accounts.Logout(r.Context(), session.Token)
		}
	}()
	code := secret()
	tag, err := h.pool.Exec(r.Context(), `INSERT INTO mcp_oauth_codes(code_hash,session_id,client_id,redirect_uri,challenge,scopes,resource,expires_at)
 SELECT $1,id,$2,$3,$4,$5,$6,now()+interval '5 minutes' FROM user_sessions WHERE token_hash=$7 AND expires_at>now()`, hash(code), a.Client.ID, a.Redirect, a.Challenge, a.Scopes, a.Resource, hash(session.Token))
	if err != nil || tag.RowsAffected() != 1 {
		serverError(w)
		return
	}
	saved = true
	h.record(r.Context(), session.User, "mcp.authorize", a.Client.ID, audit.OutcomeSuccess)
	q.Set("code", code)
	redirect.RawQuery = q.Encode()
	h.clearCSRF(w)
	http.Redirect(w, r, redirect.String(), http.StatusSeeOther)
}

func validVerifier(s string) bool {
	if len(s) < 43 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-._~", c)) {
			return false
		}
	}
	return true
}

func (h *Handler) token(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	f := r.PostForm
	if r.Header.Get("Authorization") != "" || f.Get("client_secret") != "" {
		oauthError(w, 400, "invalid_client", "Use a public client with PKCE and client_id")
		return
	}
	if resource := f.Get("resource"); resource != "" && resource != h.issuer+"/mcp" {
		oauthError(w, 400, "invalid_target", "Resource does not match this MCP server")
		return
	}
	if f.Get("client_id") == "" {
		oauthError(w, 400, "invalid_client", "client_id is required")
		return
	}
	switch f.Get("grant_type") {
	case "authorization_code":
		h.exchangeCode(w, r)
	case "refresh_token":
		h.refresh(w, r)
	default:
		oauthError(w, 400, "unsupported_grant_type", "Use authorization_code or refresh_token")
	}
}

func (h *Handler) exchangeCode(w http.ResponseWriter, r *http.Request) {
	f := r.PostForm
	if len(f.Get("code")) != 43 || !validVerifier(f.Get("code_verifier")) {
		invalidGrant(w)
		return
	}
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		serverError(w)
		return
	}
	defer tx.Rollback(r.Context())
	var sessionID, challenge, resource string
	var scopes []string
	var expires time.Time
	err = tx.QueryRow(r.Context(), `DELETE FROM mcp_oauth_codes c USING user_sessions s WHERE c.code_hash=$1 AND c.client_id=$2
 AND c.redirect_uri=$3 AND c.expires_at>now() AND s.id=c.session_id AND s.expires_at>now()
 RETURNING c.session_id::text,c.challenge,c.resource,c.scopes,s.expires_at`, hash(f.Get("code")), f.Get("client_id"), f.Get("redirect_uri")).Scan(&sessionID, &challenge, &resource, &scopes, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		invalidGrant(w)
		return
	}
	if err != nil {
		serverError(w)
		return
	}
	expected := base64.RawURLEncoding.EncodeToString(hash(f.Get("code_verifier")))
	if subtle.ConstantTimeCompare([]byte(expected), []byte(challenge)) != 1 {
		// A failed verifier consumes the code too; retry requires new consent.
		if tx.Commit(r.Context()) != nil {
			serverError(w)
			return
		}
		invalidGrant(w)
		return
	}
	access, refresh := secret(), secret()
	var grantID string
	err = tx.QueryRow(r.Context(), `INSERT INTO mcp_oauth_grants(session_id,client_id,scopes,resource,access_hash,access_expires_at,expires_at)
 VALUES($1::uuid,$2,$3,$4,$5,LEAST(now()+interval '15 minutes',$6),$6) RETURNING id::text`, sessionID, f.Get("client_id"), scopes, resource, hash(access), expires).Scan(&grantID)
	if err != nil {
		serverError(w)
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO mcp_oauth_refresh_tokens(token_hash,grant_id) VALUES($1,$2::uuid)`, hash(refresh), grantID); err != nil {
		serverError(w)
		return
	}
	if tx.Commit(r.Context()) != nil {
		serverError(w)
		return
	}
	tokenResponse(w, access, refresh, scopes, expires)
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	f := r.PostForm
	if len(f.Get("refresh_token")) != 43 {
		invalidGrant(w)
		return
	}
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		serverError(w)
		return
	}
	defer tx.Rollback(r.Context())
	var id string
	var scopes []string
	var expires time.Time
	var used bool
	err = tx.QueryRow(r.Context(), `SELECT g.id::text,g.scopes,g.expires_at,t.used FROM mcp_oauth_refresh_tokens t
 JOIN mcp_oauth_grants g ON g.id=t.grant_id JOIN user_sessions s ON s.id=g.session_id
 WHERE t.token_hash=$1 AND g.client_id=$2 AND g.resource=$3 AND g.expires_at>now() AND s.expires_at>now()
 FOR UPDATE OF g,t`, hash(f.Get("refresh_token")), f.Get("client_id"), h.issuer+"/mcp").Scan(&id, &scopes, &expires, &used)
	if errors.Is(err, pgx.ErrNoRows) {
		invalidGrant(w)
		return
	}
	if err != nil {
		serverError(w)
		return
	}
	if used {
		if _, err = tx.Exec(r.Context(), `DELETE FROM user_sessions WHERE id=(SELECT session_id FROM mcp_oauth_grants WHERE id=$1::uuid)`, id); err != nil {
			serverError(w)
			return
		}
		if tx.Commit(r.Context()) != nil {
			serverError(w)
			return
		}
		invalidGrant(w)
		return
	}
	if requested := f.Get("scope"); requested != "" {
		next := strings.Fields(requested)
		for _, s := range next {
			if !slices.Contains(scopes, s) {
				oauthError(w, 400, "invalid_scope", "Refresh cannot expand consent")
				return
			}
		}
		scopes = next
	}
	access, refresh := secret(), secret()
	if _, err = tx.Exec(r.Context(), `UPDATE mcp_oauth_refresh_tokens SET used=true WHERE token_hash=$1`, hash(f.Get("refresh_token"))); err != nil {
		serverError(w)
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE mcp_oauth_grants SET access_hash=$2,access_expires_at=LEAST(now()+interval '15 minutes',expires_at),scopes=$3 WHERE id=$1::uuid`, id, hash(access), scopes); err != nil {
		serverError(w)
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO mcp_oauth_refresh_tokens(token_hash,grant_id) VALUES($1,$2::uuid)`, hash(refresh), id); err != nil {
		serverError(w)
		return
	}
	if tx.Commit(r.Context()) != nil {
		serverError(w)
		return
	}
	tokenResponse(w, access, refresh, scopes, expires)
}

func tokenResponse(w http.ResponseWriter, access, refresh string, scopes []string, expires time.Time) {
	seconds := min(900, max(0, int(time.Until(expires).Seconds())))
	jsonResponse(w, 200, map[string]any{"access_token": access, "refresh_token": refresh, "token_type": "Bearer", "expires_in": seconds, "scope": strings.Join(scopes, " ")})
}
func invalidGrant(w http.ResponseWriter) {
	oauthError(w, 400, "invalid_grant", "Authorization expired, revoked, already used, or does not match this client")
}
func serverError(w http.ResponseWriter) {
	oauthError(w, 500, "server_error", "Could not complete authorization")
}

func (h *Handler) revoke(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	f := r.PostForm
	if len(f.Get("token")) != 43 || f.Get("client_id") == "" {
		w.WriteHeader(200)
		return
	}
	// Unknown tokens receive the same response as successfully revoked tokens.
	_, err := h.pool.Exec(r.Context(), `DELETE FROM user_sessions WHERE id IN (SELECT g.session_id FROM mcp_oauth_grants g WHERE g.client_id=$1 AND
 (g.access_hash=$2 OR g.id IN (SELECT grant_id FROM mcp_oauth_refresh_tokens WHERE token_hash=$2)))`, f.Get("client_id"), hash(f.Get("token")))
	if err != nil {
		serverError(w)
		return
	}
	w.WriteHeader(200)
}
