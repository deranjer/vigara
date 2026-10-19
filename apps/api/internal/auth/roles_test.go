package auth

import "testing"

func TestRolePermissions(t *testing.T) {
	for _, tc := range []struct {
		role string
		perm Permission
		want bool
	}{
		{RoleViewer, PermContainersRead, true},
		{RoleViewer, PermContainersManage, false},
		{RoleViewer, PermContainersExec, false},
		{RoleOperator, PermContainersManage, true},
		{RoleOperator, PermContainersExec, true},
		{RoleOperator, PermStacksDeploy, false},
		{RoleOperator, PermSecretsReveal, false},
		{RoleOperator, PermUsersManage, false},
		{RoleOperator, PermAuditRead, false},
		{RoleAdmin, PermStacksDeploy, true},
		{RoleAdmin, PermUsersManage, true},
		{RoleAdmin, PermAuditRead, true},
		{"", PermContainersRead, false},
		{"root", PermContainersRead, false},
	} {
		if got := RoleAllows(tc.role, tc.perm); got != tc.want {
			t.Errorf("RoleAllows(%q, %q) = %v, want %v", tc.role, tc.perm, got, tc.want)
		}
	}
	if ValidRole("root") || !ValidRole(RoleViewer) || !ValidRole(RoleOperator) || !ValidRole(RoleAdmin) {
		t.Error("ValidRole disagrees with the defined roles")
	}
}
