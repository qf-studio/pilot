package executor

// TeamChecker validates team permissions before task execution.
// When set on a Runner, it enforces role-based access control on every Execute() call.
// The interface uses string-typed permissions so the executor package doesn't depend on teams.
type TeamChecker interface {
	// CheckPermission verifies a member has a specific permission (e.g., "execute_tasks").
	CheckPermission(memberID string, perm string) error
	// CheckProjectAccess verifies a member can perform an action on a specific project.
	CheckProjectAccess(memberID, projectPath string, requiredPerm string) error
}
