package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/audit"
)

func adminTestService(t *testing.T) (*Service, *audit.Recorder, Session, *http.ServeMux) {
	t.Helper()
	service, pool := testAuthServiceWithMigrations(t, "000001_initial_schema.up.sql", "000002_local_auth.up.sql", "000003_agent_enrollment.up.sql", "000015_roles_and_audit.up.sql")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := service.CreateFirstUser(ctx, "owner", "Owner", "correct horse battery staple", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	recorder := audit.NewRecorder(pool)
	mux := http.NewServeMux()
	service.RegisterAdmin(mux, recorder)
	return service, recorder, admin, mux
}

func call(mux http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if token != "" {
		req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token})
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestRoleManagementEnforcesPermissionsAndKeepsAnAdministrator(t *testing.T) {
	service, recorder, admin, mux := adminTestService(t)
	ctx := context.Background()

	if rec := call(mux, http.MethodGet, "/api/users", "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous list returned %d", rec.Code)
	}

	rec := call(mux, http.MethodPost, "/api/users", `{"username":"watcher","displayName":"Watcher","password":"another long password","role":"viewer"}`, admin.Token)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create viewer returned %d: %s", rec.Code, rec.Body.String())
	}
	var viewer ManagedUser
	if err := json.Unmarshal(rec.Body.Bytes(), &viewer); err != nil || viewer.Role != RoleViewer {
		t.Fatalf("created viewer = %+v, %v", viewer, err)
	}
	if strings.Contains(rec.Body.String(), "password") {
		t.Fatalf("response leaked credential fields: %s", rec.Body.String())
	}
	if rec := call(mux, http.MethodPost, "/api/users", `{"username":"watcher","displayName":"Dup","password":"another long password","role":"viewer"}`, admin.Token); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate username returned %d", rec.Code)
	}
	if rec := call(mux, http.MethodPost, "/api/users", `{"username":"bad","displayName":"Bad","password":"another long password","role":"root"}`, admin.Token); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown role returned %d", rec.Code)
	}
	if rec := call(mux, http.MethodPost, "/api/users", `{"username":"short","displayName":"Short","password":"tiny","role":"viewer"}`, admin.Token); rec.Code != http.StatusBadRequest {
		t.Fatalf("short password returned %d", rec.Code)
	}

	// A viewer signs in but cannot manage users or read the audit log.
	session, err := service.Login(ctx, "watcher", "another long password")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/users", "/api/audit"} {
		if rec := call(mux, http.MethodGet, path, "", session.Token); rec.Code != http.StatusForbidden {
			t.Fatalf("viewer GET %s returned %d, want 403", path, rec.Code)
		}
	}
	if rec := call(mux, http.MethodPost, "/api/users", `{"username":"sneaky","displayName":"Sneaky","password":"another long password","role":"admin"}`, session.Token); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer create returned %d, want 403", rec.Code)
	}
	if rec := call(mux, http.MethodPatch, "/api/users/"+viewer.ID, `{"role":"admin"}`, session.Token); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer self-promotion returned %d, want 403", rec.Code)
	}

	// Promote to operator, then demote again; lowering a role revokes its sessions.
	if rec := call(mux, http.MethodPatch, "/api/users/"+viewer.ID, `{"role":"operator"}`, admin.Token); rec.Code != http.StatusOK {
		t.Fatalf("promote returned %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := service.Authenticate(ctx, session.Token); err != nil {
		t.Fatalf("session was revoked by a promotion: %v", err)
	}
	if rec := call(mux, http.MethodPatch, "/api/users/"+viewer.ID, `{"role":"viewer"}`, admin.Token); rec.Code != http.StatusOK {
		t.Fatalf("demote returned %d", rec.Code)
	}
	if _, err := service.Authenticate(ctx, session.Token); err == nil {
		t.Fatal("session survived a role reduction")
	}

	// The last administrator cannot be demoted, and bad IDs are plain not-founds.
	if rec := call(mux, http.MethodPatch, "/api/users/"+admin.User.ID, `{"role":"viewer"}`, admin.Token); rec.Code != http.StatusConflict {
		t.Fatalf("demoting the last admin returned %d, want 409", rec.Code)
	}
	if rec := call(mux, http.MethodPatch, "/api/users/not-a-uuid", `{"role":"viewer"}`, admin.Token); rec.Code != http.StatusNotFound {
		t.Fatalf("malformed id returned %d, want 404", rec.Code)
	}
	if _, err := service.SetUserRole(ctx, admin.User.ID, RoleViewer); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("SetUserRole last admin error = %v", err)
	}

	// Every change above was audited, including failures, and is readable by admins.
	rec = call(mux, http.MethodGet, "/api/audit?limit=100", "", admin.Token)
	if rec.Code != http.StatusOK {
		t.Fatalf("audit list returned %d: %s", rec.Code, rec.Body.String())
	}
	var entries []audit.Entry
	if err := json.Unmarshal(rec.Body.Bytes(), &entries); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, entry := range entries {
		counts[entry.Action+"/"+entry.Outcome]++
		if entry.ActorName != "owner" {
			t.Errorf("entry actor = %q", entry.ActorName)
		}
	}
	if counts["user.create/success"] != 1 || counts["user.create/failure"] != 3 || counts["user.role/success"] != 2 || counts["user.role/failure"] != 2 {
		t.Fatalf("audit counts = %v", counts)
	}
	page, err := recorder.List(ctx, audit.ListFilter{Limit: 2})
	if err != nil || len(page) != 2 || page[0].ID <= page[1].ID {
		t.Fatalf("page = %+v, %v", page, err)
	}
	older, err := recorder.List(ctx, audit.ListFilter{Limit: 100, BeforeID: page[1].ID})
	if err != nil || len(older) != len(entries)-2 {
		t.Fatalf("older page has %d entries (err %v), want %d", len(older), err, len(entries)-2)
	}
}

func TestWriteGuardBlocksNonAdminWritesOnLegacyRoutes(t *testing.T) {
	service, _, admin, mux := adminTestService(t)
	ctx := context.Background()
	if rec := call(mux, http.MethodPost, "/api/users", `{"username":"oper","displayName":"Oper","password":"another long password","role":"operator"}`, admin.Token); rec.Code != http.StatusCreated {
		t.Fatalf("create operator returned %d", rec.Code)
	}
	operator, err := service.Login(ctx, "oper", "another long password")
	if err != nil {
		t.Fatal(err)
	}
	reached := 0
	guarded := service.WriteGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { reached++; w.WriteHeader(http.StatusNoContent) }))
	for _, tc := range []struct {
		method, path, token string
		want                int
	}{
		{http.MethodGet, "/api/agents/anything", operator.Token, http.StatusNoContent},
		{http.MethodPost, "/api/agents/enrollment-tokens", operator.Token, http.StatusForbidden},
		{http.MethodPost, "/api/agent-downloads", operator.Token, http.StatusForbidden},
		{http.MethodDelete, "/api/backups/policies/x", operator.Token, http.StatusForbidden},
		{http.MethodPost, "/api/agents/enrollment-tokens", admin.Token, http.StatusNoContent},
		{http.MethodPost, "/api/auth/logout", operator.Token, http.StatusNoContent},
		{http.MethodPost, "/api/channel/anything", operator.Token, http.StatusNoContent},
		{http.MethodPost, "/api/containers/agent/web/actions", operator.Token, http.StatusNoContent},
		{http.MethodDelete, "/api/containersx", operator.Token, http.StatusForbidden},
		{http.MethodPost, "/api/agent/poll", "", http.StatusNoContent},
		{http.MethodPost, "/api/agents/enrollment-tokens", "", http.StatusNoContent},
		{http.MethodPost, "/api/agents/enrollment-tokens", "stale-token", http.StatusNoContent},
	} {
		if rec := call(guarded, tc.method, tc.path, "", tc.token); rec.Code != tc.want {
			t.Errorf("%s %s (token set: %v) returned %d, want %d", tc.method, tc.path, tc.token != "", rec.Code, tc.want)
		}
	}
	if reached != 8 {
		t.Errorf("guarded handler ran %d times, want 8", reached)
	}
}
