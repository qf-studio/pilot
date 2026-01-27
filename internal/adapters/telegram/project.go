package telegram

// ProjectInfo represents a project configuration (avoids import cycle with config)
type ProjectInfo struct {
	Name          string
	Path          string
	Navigator     bool
	DefaultBranch string
}

// ProjectSource provides project lookup methods (avoids import cycle with config)
type ProjectSource interface {
	GetProjectByName(name string) *ProjectInfo
	GetProjectByPath(path string) *ProjectInfo
	GetDefaultProject() *ProjectInfo
	ListProjects() []*ProjectInfo
}
