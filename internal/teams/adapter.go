package teams

// ServiceAdapter wraps teams.Service to satisfy executor.TeamChecker interface (GH-634).
// It converts string-typed permissions to teams.Permission, decoupling the executor
// package from direct dependency on the teams package.
type ServiceAdapter struct {
	service *Service
}

// NewServiceAdapter creates a ServiceAdapter wrapping the given teams service.
func NewServiceAdapter(service *Service) *ServiceAdapter {
	return &ServiceAdapter{service: service}
}

// CheckPermission verifies a member has a specific permission.
// The perm string is cast to teams.Permission (e.g., "execute_tasks" → PermExecuteTasks).
func (a *ServiceAdapter) CheckPermission(memberID string, perm string) error {
	return a.service.CheckPermission(memberID, Permission(perm))
}

// CheckProjectAccess verifies a member can perform an action on a specific project.
// Checks both the base permission and project-level access restrictions.
func (a *ServiceAdapter) CheckProjectAccess(memberID, projectPath string, requiredPerm string) error {
	return a.service.CheckProjectAccess(memberID, projectPath, Permission(requiredPerm))
}
