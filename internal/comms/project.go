package comms

// ProjectInfo represents a project configuration (avoids import cycle with config).
type ProjectInfo struct {
	Name          string
	Path          string
	Navigator     bool
	DefaultBranch string
	// BranchFrom mirrors config.ProjectConfig.BranchFrom; it wins over
	// DefaultBranch (see ResolveBaseBranch).
	BranchFrom string
	// Canary mirrors config.ProjectConfig.Canary (GH-4240/GH-4648) so
	// chat-triggered tasks (comms.Handler) can stamp Task.IsCanary at
	// construction time the same way the poller handlers do via
	// cmd/pilot/handler_common.go's handleIssueGeneric.
	Canary bool
	// Repo is the GitHub "owner/name" when the project has a github block,
	// else empty.
	Repo string
}

// ResolveBaseBranch mirrors config.ProjectConfig.ResolveBaseBranch: BranchFrom
// takes precedence over DefaultBranch; both may be empty (the worktree manager
// then detects origin/HEAD). A nil receiver resolves to "".
func (p *ProjectInfo) ResolveBaseBranch() string {
	if p == nil {
		return ""
	}
	if p.BranchFrom != "" {
		return p.BranchFrom
	}
	return p.DefaultBranch
}

// GetName satisfies duck-typed interface used by CommandHandler.handleProjects.
func (p *ProjectInfo) GetName() string { return p.Name }

// GetPath satisfies duck-typed interface used by CommandHandler.handleProjects.
func (p *ProjectInfo) GetPath() string { return p.Path }

// IsNavigator satisfies duck-typed interface used by CommandHandler.handleProjects.
func (p *ProjectInfo) IsNavigator() bool { return p.Navigator }

// ProjectSource provides project lookup methods (avoids import cycle with config).
type ProjectSource interface {
	GetProjectByName(name string) *ProjectInfo
	GetProjectByPath(path string) *ProjectInfo
	GetDefaultProject() *ProjectInfo
	ListProjects() []*ProjectInfo
}
