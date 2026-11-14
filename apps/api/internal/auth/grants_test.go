package auth

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestResourceGrantIdentitiesAndPermissions(t *testing.T) {
	g := ResourceGrant{AgentID: "host", Kind: "service", Resource: "dragonwilds", Service: "game", Restart: true}
	if !g.Matches("host", "old-id", "dragonwilds", "game") || !g.Matches("host", "replacement-id", "dragonwilds", "game") {
		t.Fatal("service grant must follow replacement IDs")
	}
	for _, args := range [][4]string{{"other-host", "old-id", "dragonwilds", "game"}, {"host", "old-id", "other-project", "game"}, {"host", "old-id", "dragonwilds", "database"}} {
		if g.Matches(args[0], args[1], args[2], args[3]) {
			t.Fatalf("grant crossed resource boundary: %v", args)
		}
	}
	if !g.Allows("view") || !g.Allows("restart") || g.Allows("logs") || g.Allows("recreate") || g.Allows("pull-recreate") || g.Allows("exec") {
		t.Fatal("operation boundaries failed")
	}
	g.Kind = "container"
	g.Resource = strings.Repeat("a", 64)
	g.Service = ""
	if g.Matches("host", strings.Repeat("b", 64), "dragonwilds", "game") {
		t.Fatal("standalone grant followed an unrelated replacement")
	}
}
func TestRestrictedRoutesDefaultDeny(t *testing.T) {
	prefix := "/api/containers/11111111-1111-4111-8111-111111111111/" + strings.Repeat("a", 64)
	for _, path := range []string{"/api/devices", "/api/channel/agents", "/api/stacks", "/api/docker/host/images", "/api/users", "/api/agent/enroll", "/api/new-feature", prefix + "/exec", prefix + "/values/preview"} {
		if restrictedRoute(http.MethodGet, path) {
			t.Errorf("allowed %s", path)
		}
	}
	for _, path := range []string{"/api/containers", prefix + "/inspect", prefix + "/logs/search", prefix + "/values", "/api/auth/me"} {
		if !restrictedRoute(http.MethodGet, path) {
			t.Errorf("blocked %s", path)
		}
	}
	if restrictedRoute(http.MethodDelete, prefix+"/values") {
		t.Fatal("restricted account may delete saved values")
	}
	if RoleAllows(RoleRestricted, PermContainersManage) || RoleAllows(RoleRestricted, PermSecretsReveal) || RoleAllows(RoleRestricted, PermDevicesRead) {
		t.Fatal("restricted role received global privileges")
	}
}
func TestResourceGrantsRevokeSessionsAndBlockLegacyReadRoutes(t *testing.T) {
	service, _, admin, mux := adminTestService(t)
	ctx := context.Background()
	created, err := service.CreateUser(ctx, "friend", "Friend", "a long friend password", RoleRestricted)
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.Login(ctx, "friend", "a long friend password")
	if err != nil {
		t.Fatal(err)
	}
	guarded := service.WriteGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, path := range []string{"/api/devices", "/api/stacks", "/api/channel/agents", "/api/users", "/api/docker/host/images"} {
		if rec := call(guarded, "GET", path, "", session.Token); rec.Code != 403 {
			t.Fatalf("restricted %s returned %d", path, rec.Code)
		}
	}
	if rec := call(guarded, "GET", "/api/containers", "", session.Token); rec.Code != 204 {
		t.Fatalf("fleet route %d", rec.Code)
	}
	if rec := call(mux, "PUT", "/api/users/"+created.ID+"/resources", `{"grants":[]}`, admin.Token); rec.Code != 204 {
		t.Fatalf("save access: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := service.Authenticate(ctx, session.Token); err == nil {
		t.Fatal("grant change did not revoke session")
	}
	if _, err := service.Login(ctx, "friend", "a long friend password"); err != nil {
		t.Fatal("user cannot sign in after access change", err)
	}
}
