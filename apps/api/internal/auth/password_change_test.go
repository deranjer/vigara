package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

func TestChangePasswordPreservesWhitespaceAndRotatesSessions(t *testing.T) {
	service, _ := testAuthService(t)
	ctx := context.Background()
	const old = " original long passphrase "
	const next = "\t new passphrase with spaces \u00a0"
	first, err := service.CreateFirstUser(ctx, "owner", "Owner", old, old)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Login(ctx, "owner", old)
	if err != nil {
		t.Fatal(err)
	}
	other, err := service.CreateUser(ctx, "other", "Other user", "different long password", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	otherSession, err := service.Login(ctx, other.Username, "different long password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.ChangePassword(ctx, first.Token, strings.TrimSpace(old), next, next); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("whitespace in current password was ignored: %v", err)
	}
	if _, err = service.ChangePassword(ctx, first.Token, old, next, next+"x"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("confirmation mismatch accepted: %v", err)
	}
	if _, err = service.ChangePassword(ctx, first.Token, old, "short", "short"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("short password accepted: %v", err)
	}
	if _, err = service.Authenticate(ctx, first.Token); err != nil {
		t.Fatal("failed password change revoked session", err)
	}
	changed, err := service.ChangePassword(ctx, first.Token, old, next, next)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{first.Token, second.Token} {
		if _, err = service.Authenticate(ctx, token); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("old session survived password change: %v", err)
		}
	}
	if _, err = service.Authenticate(ctx, changed.Token); err != nil {
		t.Fatal("replacement session invalid", err)
	}
	if _, err = service.Authenticate(ctx, otherSession.Token); err != nil {
		t.Fatal("another account was signed out", err)
	}
	for _, password := range []string{old, strings.TrimSpace(next)} {
		if _, err = service.Login(ctx, "owner", password); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("wrong password accepted: %v", err)
		}
	}
	if _, err = service.Login(ctx, "owner", next); err != nil {
		t.Fatal("exact password rejected", err)
	}
}

func TestPasswordChangeHTTPRequiresOwnSessionAndSameOrigin(t *testing.T) {
	service, pool := testAuthService(t)
	ctx := context.Background()
	first, err := service.CreateFirstUser(ctx, "owner", "Owner", "original passphrase", "original passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `ALTER TABLE users DROP CONSTRAINT users_role_check;ALTER TABLE users ADD CONSTRAINT users_role_check CHECK(role IN ('viewer','operator','admin'))`); err != nil {
		t.Fatal(err)
	}
	viewer, err := service.CreateUser(ctx, "viewer", "Viewer", "viewer original password", RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	signedIn, err := service.Login(ctx, viewer.Username, "viewer original password")
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Use(service.SessionMiddleware)
	router.Use(service.WriteGuard)
	RegisterAPI(humachi.New(router, huma.DefaultConfig("Test", "1")), service, true)
	call := func(token, origin, current, new, confirmation string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"currentPassword": current, "newPassword": new, "confirmPassword": confirmation})
		req := httptest.NewRequest(http.MethodPost, "http://vigara.test/api/auth/password", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token})
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	if w := call("", "", "wrong", "a new long password", "a new long password"); w.Code != 401 {
		t.Fatalf("anonymous: %d", w.Code)
	}
	if w := call(signedIn.Token, "https://evil.test", "viewer original password", "a new long password", "a new long password"); w.Code != 403 {
		t.Fatalf("cross-origin: %d", w.Code)
	}
	if w := call(signedIn.Token, "http://vigara.test", "wrong password", "a new long password", "a new long password"); w.Code != 400 {
		t.Fatalf("wrong current: %d", w.Code)
	}
	if w := call(signedIn.Token, "http://vigara.test", "viewer original password", "a new long password", "mismatched long password"); w.Code != 400 {
		t.Fatalf("mismatch: %d", w.Code)
	}
	w := call(signedIn.Token, "http://vigara.test", "viewer original password", "a new long password", "a new long password")
	if w.Code != 200 {
		t.Fatalf("viewer password change: %d %s", w.Code, w.Body)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Value == signedIn.Token || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("replacement session cookie was not protected")
	}
	if strings.Contains(w.Body.String(), "a new long password") || strings.Contains(w.Body.String(), "viewer original password") {
		t.Fatal("password echoed in response")
	}
	if _, err = service.Authenticate(ctx, cookies[0].Value); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Authenticate(ctx, first.Token); err != nil {
		t.Fatal("changing own password signed out another account", err)
	}
	if w = call(signedIn.Token, "http://vigara.test", "viewer original password", "another long password", "another long password"); w.Code != 401 {
		t.Fatalf("revoked session accepted: %d", w.Code)
	}
}

func TestConcurrentPasswordChangesCannotUseRevokedSession(t *testing.T) {
	service, _ := testAuthService(t)
	ctx := context.Background()
	first, err := service.CreateFirstUser(ctx, "owner", "Owner", "original long password", "original long password")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Login(ctx, "owner", "original long password")
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		session Session
		err     error
	}
	results := make(chan outcome, 2)
	start := make(chan struct{})
	for _, token := range []string{first.Token, second.Token} {
		go func(token string) {
			<-start
			s, e := service.ChangePassword(ctx, token, "original long password", "new long password", "new long password")
			results <- outcome{s, e}
		}(token)
	}
	close(start)
	winners := 0
	for range 2 {
		result := <-results
		if result.err == nil {
			winners++
			if _, err = service.Authenticate(ctx, result.session.Token); err != nil {
				t.Fatal(err)
			}
		} else if !errors.Is(result.err, pgx.ErrNoRows) && !errors.Is(result.err, ErrInvalidCredentials) {
			t.Fatal(result.err)
		}
	}
	if winners != 1 {
		t.Fatalf("%d concurrent password changes succeeded", winners)
	}
}

func TestLoginCannotIssueSessionUsingPasswordChangedDuringLogin(t *testing.T) {
	service, pool := testAuthService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	first, err := service.CreateFirstUser(ctx, "owner", "Owner", "original long password", "original long password")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT id FROM users WHERE id=$1::uuid FOR UPDATE`, first.User.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, e := service.Login(ctx, "owner", "original long password"); done <- e }()
	// Wait until the verified login reaches session issuance and waits for the account lock.
	waiting := false
	for range 200 {
		if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'SELECT password_hash,username,display_name,role%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("login did not wait on the account lock")
	}
	hash, err := hashPassword("replacement long password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET password_hash=$2 WHERE id=$1::uuid`, first.User.ID, hash); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM user_sessions WHERE user_id=$1::uuid`, first.User.ID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("login issued a session after password changed: %v", err)
	}
}
