package executor

// TeamChecker is an interface for checking team-level permissions before task execution.
// This interface is satisfied by an adapter wrapping teams.Service, allowing the executor
// to enforce team RBAC without importing the teams package directly (avoiding import cycles).
type TeamChecker interface {
	// CanExecute checks whether the given team member is allowed to execute a task
	// on the specified project. Returns nil if allowed, or an error describing the
	// denial reason (e.g. permission denied, member not found, project not allowed).
	CanExecute(memberID, projectPath string) error
}
