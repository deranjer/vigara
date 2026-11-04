package stacks

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/audit"
	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/apps/api/internal/githubapp"
	"github.com/jackc/pgx/v5"
)

const githubCallback = "/api/stacks/settings/github/callback"
const githubInstalled = "/api/stacks/settings/github/installed"

var githubContinue = template.Must(template.New("github-continue").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Continue GitHub setup · Vigara</title><body><main><h1>Continue GitHub setup</h1><p>Return to your signed-in Vigara session to finish connecting the App.</p><p><a href="{{.Next}}">Continue GitHub setup</a></p></main><script nonce="{{.Nonce}}">window.location.replace({{.Next}})</script></body></html>`))

// Login cookies remain SameSite=Strict. A document on our own origin starts a
// new same-site navigation after GitHub's cross-site redirect. The link also
// works when scripts are disabled. No code is exchanged on this landing page.
func githubContinuation(w http.ResponseWriter, r *http.Request, path string) bool {
	if sessionHash(r) != "" || r.URL.Query().Get("continue") == "1" {
		return false
	}
	nonce, err := setupState()
	if err != nil {
		http.Error(w, "Could not continue GitHub setup.", 500)
		return true
	}
	q := url.Values{"continue": {"1"}}
	for _, key := range []string{"state", "code", "installation_id"} {
		if value := r.URL.Query().Get(key); value != "" && len(value) <= 200 {
			q.Set(key, value)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'nonce-"+nonce+"'; frame-ancestors 'none'; base-uri 'none'")
	_ = githubContinue.Execute(w, struct{ Next, Nonce string }{path + "?" + q.Encode(), nonce})
	return true
}

var githubName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)

func githubPublicURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return "", errors.New("Set VIGIRA_PUBLIC_URL to this server's HTTPS origin before connecting GitHub")
	}
	return u.String(), nil
}

func digestValue(v string) string { sum := sha256.Sum256([]byte(v)); return hex.EncodeToString(sum[:]) }
func setupState() (string, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	return hex.EncodeToString(b), err
}
func sessionHash(r *http.Request) string {
	cookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil || cookie.Value == "" {
		return ""
	}
	return digestValue(cookie.Value)
}

func (a *API) githubAdmin(w http.ResponseWriter, r *http.Request, mutation bool) (auth.User, bool) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	u, ok := a.Authz.Authorize(w, r, auth.PermUsersManage)
	if !ok {
		return u, false
	}
	if sessionHash(r) == "" {
		http.Error(w, "Sign in as an administrator in this browser to connect GitHub.", 401)
		return u, false
	}
	if mutation && !auth.SameOrigin(r) {
		http.Error(w, "Use this server's Git settings to connect GitHub.", 403)
		return u, false
	}
	if a.Pool == nil || !a.GitHub.Available() {
		http.Error(w, "GitHub App setup requires VIGIRA_GITHUB_APP_MASTER_KEY on the server.", 503)
		return u, false
	}
	return u, true
}

func (a *API) startGitHubSetup(w http.ResponseWriter, r *http.Request) {
	u, ok := a.githubAdmin(w, r, true)
	if !ok {
		return
	}
	base, err := githubPublicURL(a.PublicURL)
	if err != nil {
		http.Error(w, err.Error(), 422)
		return
	}
	var q struct {
		Remote       string `json:"remote"`
		Branch       string `json:"branch"`
		Organization string `json:"organization"`
	}
	if !decode(w, r, &q) {
		return
	}
	if q.Branch == "" {
		q.Branch = "main"
	}
	if len(q.Branch) > 200 || strings.ContainsAny(q.Branch, "\r\n\x00") {
		http.Error(w, "Enter a valid branch name.", 422)
		return
	}
	if _, _, err := githubapp.Repository(q.Remote); err != nil {
		http.Error(w, "Use https://github.com/OWNER/REPOSITORY.git for GitHub App authentication.", 422)
		return
	}
	if q.Organization != "" && !githubName.MatchString(q.Organization) {
		http.Error(w, "Enter a valid GitHub organization name or leave it empty for a personal App.", 422)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	unlock, err := a.repoLock(ctx)
	if err != nil {
		fail(w, err)
		return
	}
	defer unlock()
	var connected bool
	if err := a.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM stack_github_app)").Scan(&connected); err != nil {
		fail(w, err)
		return
	}
	if connected {
		http.Error(w, "An App is already connected. Disconnect it before registering a replacement.", 409)
		return
	}
	state, err := setupState()
	if err != nil {
		fail(w, err)
		return
	}
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		fail(w, err)
		return
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "DELETE FROM stack_github_app_setups WHERE expires_at < now() OR user_id=$1", u.ID); err == nil {
		_, err = tx.Exec(ctx, "INSERT INTO stack_github_app_setups(state_hash,user_id,session_hash,phase,remote,branch,expires_at) VALUES($1,$2,$3,'register',$4,$5,now()+interval '1 hour')", digestValue(state), u.ID, sessionHash(r), q.Remote, q.Branch)
	}
	if err != nil {
		fail(w, err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		fail(w, err)
		return
	}
	action := "https://github.com/settings/apps/new"
	if q.Organization != "" {
		action = "https://github.com/organizations/" + q.Organization + "/settings/apps/new"
	}
	manifest := map[string]any{
		"name": "Vigara-" + state[:8], "url": base,
		"description":  "Save Compose stacks directly to Git. Deployment is a separate action.",
		"redirect_url": base + githubCallback,
		"setup_url":    base + githubInstalled + "?state=" + state,
		"public":       false, "hook_attributes": map[string]any{"url": base + "/api/stacks/settings/github/events", "active": false},
		"default_permissions": map[string]string{"contents": "write"},
		"default_events":      []string{}, "request_oauth_on_install": false,
	}
	a.record(ctx, u, "repository", "github-app-registration-started", audit.OutcomeSuccess, nil)
	respond(w, 200, map[string]any{"action": action + "?state=" + state, "manifest": manifest})
}

func (a *API) claimGitHubSetup(ctx context.Context, r *http.Request, user auth.User, from, to string) (string, []byte, error) {
	state := r.URL.Query().Get("state")
	if len(state) != 64 {
		return "", nil, errors.New("invalid setup state")
	}
	var remote string
	var sealed []byte
	err := a.Pool.QueryRow(ctx, `UPDATE stack_github_app_setups SET phase=$1 WHERE state_hash=$2 AND user_id=$3 AND session_hash=$4 AND phase=$5 AND expires_at>now() RETURNING remote,credentials`, to, digestValue(state), user.ID, sessionHash(r), from).Scan(&remote, &sealed)
	return remote, sealed, err
}

func (a *API) githubRegistrationCallback(w http.ResponseWriter, r *http.Request) {
	if githubContinuation(w, r, githubCallback) {
		return
	}
	u, ok := a.githubAdmin(w, r, false)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	unlock, err := a.repoLock(ctx)
	if err != nil {
		fail(w, err)
		return
	}
	defer unlock()
	_, _, err = a.claimGitHubSetup(ctx, r, u, "register", "converting")
	if err != nil {
		http.Error(w, "GitHub setup expired or was already used. Reopen Git settings and start again.", 400)
		return
	}
	credentials, err := a.GitHub.Convert(ctx, r.URL.Query().Get("code"))
	if err != nil {
		http.Error(w, "Could not register the GitHub App. Start setup again in Git settings.", 422)
		return
	}
	credentials.SetupState = r.URL.Query().Get("state")
	sealed, err := a.GitHub.Seal(credentials)
	if err != nil {
		fail(w, err)
		return
	}
	state := r.URL.Query().Get("state")
	if _, err = a.Pool.Exec(ctx, "UPDATE stack_github_app_setups SET credentials=$1,phase='install' WHERE state_hash=$2 AND phase='converting'", sealed, digestValue(state)); err != nil {
		fail(w, err)
		return
	}
	http.Redirect(w, r, githubInstallURL(credentials.Slug, state), http.StatusSeeOther)
}

func githubInstallURL(slug, state string) string {
	return "https://github.com/apps/" + url.PathEscape(slug) + "/installations/new?state=" + url.QueryEscape(state)
}

// A lost installation tab can be resumed without creating another App.
func (a *API) resumeGitHubInstall(w http.ResponseWriter, r *http.Request) {
	u, ok := a.githubAdmin(w, r, true)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var sealed []byte
	err := a.Pool.QueryRow(ctx, `SELECT credentials FROM stack_github_app_setups WHERE user_id=$1 AND session_hash=$2 AND phase='install' AND expires_at>now() ORDER BY created_at DESC LIMIT 1`, u.ID, sessionHash(r)).Scan(&sealed)
	if err != nil {
		http.Error(w, "No pending installation. Start GitHub setup again.", 409)
		return
	}
	v, err := a.GitHub.Open(sealed)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, 200, map[string]string{"url": githubInstallURL(v.Slug, v.SetupState)})
}

func (a *API) githubInstallationCallback(w http.ResponseWriter, r *http.Request) {
	if githubContinuation(w, r, githubInstalled) {
		return
	}
	u, ok := a.githubAdmin(w, r, false)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	unlock, err := a.repoLock(ctx)
	if err != nil {
		fail(w, err)
		return
	}
	defer unlock()
	remote, sealed, err := a.claimGitHubSetup(ctx, r, u, "install", "finishing")
	if err != nil {
		http.Error(w, "GitHub setup expired or was already used. Reopen Git settings.", 400)
		return
	}
	stateHash := digestValue(r.URL.Query().Get("state"))
	// Allow another attempt after a denied install or transient GitHub failure.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = a.Pool.Exec(cleanup, "UPDATE stack_github_app_setups SET phase='install' WHERE state_hash=$1 AND phase='finishing'", stateHash)
	}()
	v, err := a.GitHub.Open(sealed)
	if err != nil {
		fail(w, err)
		return
	}
	v.InstallationID, err = strconv.ParseInt(r.URL.Query().Get("installation_id"), 10, 64)
	if err != nil || v.InstallationID <= 0 {
		http.Error(w, "GitHub installation was not completed. Reopen Git settings to resume.", 422)
		return
	}
	if err = a.GitHub.ValidateInstallation(ctx, v, remote); err != nil {
		http.Error(w, "Install this App on the selected repository with Contents write permission, then resume setup in Git settings.", 422)
		return
	}
	v.SetupState = ""
	sealed, err = a.GitHub.Seal(v)
	if err != nil {
		fail(w, err)
		return
	}
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		fail(w, err)
		return
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, "INSERT INTO stack_github_app(singleton,app_id,slug,installation_id,remote,branch,credentials) SELECT true,$1,$2,$3,remote,branch,$4 FROM stack_github_app_setups WHERE state_hash=$5 AND phase='finishing'", v.ID, v.Slug, v.InstallationID, sealed, stateHash)
	if err == nil {
		_, err = tx.Exec(ctx, "DELETE FROM stack_github_app_setups WHERE state_hash=$1", stateHash)
	}
	if err != nil {
		fail(w, err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		fail(w, err)
		return
	}
	a.record(ctx, u, "repository", "github-app-connected", audit.OutcomeSuccess, map[string]any{"appId": v.ID, "installationId": v.InstallationID})
	http.Redirect(w, r, "/?github_app=connected#/stacks", http.StatusSeeOther)
}

func (a *API) githubStatus(ctx context.Context, r *http.Request) (map[string]any, error) {
	_, urlErr := githubPublicURL(a.PublicURL)
	status := map[string]any{"available": a.GitHub.Available() && urlErr == nil, "connected": false, "pending": false}
	if !a.GitHub.Available() {
		status["setupHelp"] = "Set VIGIRA_GITHUB_APP_MASTER_KEY to a base64-encoded random 32-byte key on the server."
	} else if urlErr != nil {
		status["setupHelp"] = urlErr.Error()
	}
	if a.Pool == nil {
		return status, nil
	}
	var slug, remote, branch string
	var appID, installationID int64
	err := a.Pool.QueryRow(ctx, "SELECT app_id,slug,installation_id,remote,branch FROM stack_github_app WHERE singleton=true").Scan(&appID, &slug, &installationID, &remote, &branch)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		status["connected"] = true
		status["slug"] = slug
		status["appId"] = appID
		status["installationId"] = installationID
		status["remote"] = remote
		status["branch"] = branch
	}
	var pending bool
	err = a.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM stack_github_app_setups WHERE session_hash=$1 AND phase='install' AND expires_at>now())", sessionHash(r)).Scan(&pending)
	status["pending"] = pending
	return status, err
}

func (a *API) githubAuth(ctx context.Context, remote string) ([]string, error) {
	if a.Pool == nil || !a.GitHub.Available() {
		return nil, githubapp.ErrUnavailable
	}
	var sealed []byte
	if err := a.Pool.QueryRow(ctx, "SELECT credentials FROM stack_github_app WHERE singleton=true").Scan(&sealed); err != nil {
		return nil, githubapp.ErrUnavailable
	}
	return a.GitHub.GitEnvironment(ctx, sealed, remote)
}

func (a *API) disconnectGitHub(w http.ResponseWriter, r *http.Request) {
	u, ok := a.githubAdmin(w, r, true)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	unlock, err := a.repoLock(ctx)
	if err != nil {
		fail(w, err)
		return
	}
	defer unlock()
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		fail(w, err)
		return
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "UPDATE stack_git_settings SET auth_mode='server',version=version+1 WHERE auth_mode='github-app'"); err == nil {
		_, err = tx.Exec(ctx, "DELETE FROM stack_github_app")
	}
	if err == nil {
		_, err = tx.Exec(ctx, "DELETE FROM stack_github_app_setups")
	}
	if err != nil {
		fail(w, err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		fail(w, err)
		return
	}
	a.record(ctx, u, "repository", "github-app-disconnected", audit.OutcomeSuccess, nil)
	respond(w, 200, map[string]string{"message": "App credentials removed from Vigara. Uninstall or delete the App in GitHub to revoke its access there."})
}
