package comms

// ProjectInfo represents a project configuration (avoids import cycle with config).
type ProjectInfo struct {
	Name          string
	Path          string
	Navigator     bool
	DefaultBranch string
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
