package auth

// Roles, from least to most privileged.
const (
	PermDevicesRead Permission = "devices.read"
	PermBackupsRead Permission = "backups.read"
	PermAlertsRead  Permission = "alerts.read"
	RoleViewer                 = "viewer"
	RoleOperator               = "operator"
	RoleAdmin                  = "admin"
)

// Permission names one kind of action. Handlers must check the specific
// permission they need rather than comparing roles.
type Permission string

const (
	PermContainersRead   Permission = "containers.read"
	PermContainersManage Permission = "containers.manage"
	PermContainersExec   Permission = "containers.exec"
	PermStacksDeploy     Permission = "stacks.deploy"
	PermSecretsReveal    Permission = "secrets.reveal"
	PermHostsManage      Permission = "hosts.manage"
	PermUsersManage      Permission = "users.manage"
	PermAuditRead        Permission = "audit.read"
)

var rolePermissions = map[string]map[Permission]bool{
	RoleViewer:   {PermDevicesRead: true, PermBackupsRead: true, PermAlertsRead: true, PermContainersRead: true},
	RoleOperator: {PermDevicesRead: true, PermBackupsRead: true, PermAlertsRead: true, PermContainersRead: true, PermContainersManage: true, PermContainersExec: true},
	RoleAdmin: {PermDevicesRead: true, PermBackupsRead: true, PermAlertsRead: true, PermContainersRead: true, PermContainersManage: true, PermContainersExec: true, PermStacksDeploy: true,
		PermSecretsReveal: true, PermHostsManage: true, PermUsersManage: true, PermAuditRead: true},
}

// ValidRole reports whether role is one of the defined roles.
func ValidRole(role string) bool { _, ok := rolePermissions[role]; return ok }

// RoleAllows reports whether role grants permission. Unknown roles grant nothing.
func RoleAllows(role string, permission Permission) bool { return rolePermissions[role][permission] }

// Can reports whether the user's role grants permission.
func (u User) Can(permission Permission) bool { return RoleAllows(u.Role, permission) }
