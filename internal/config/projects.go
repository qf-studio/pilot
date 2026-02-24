package config

import (
	"github.com/alekspetrov/pilot/internal/comms"
)

// ProjectSourceAdapter wraps Config to implement comms.ProjectSource.
type ProjectSourceAdapter struct {
	cfg *Config
}

// NewProjectSource creates a new comms.ProjectSource from Config.
func NewProjectSource(cfg *Config) comms.ProjectSource {
	if cfg == nil {
		return nil
	}
	return &ProjectSourceAdapter{cfg: cfg}
}

// NewSlackProjectSource creates a comms.ProjectSource for Slack (same adapter).
func NewSlackProjectSource(cfg *Config) comms.ProjectSource {
	return NewProjectSource(cfg)
}

// GetProjectByName returns project info by name.
func (a *ProjectSourceAdapter) GetProjectByName(name string) *comms.ProjectInfo {
	proj := a.cfg.GetProjectByName(name)
	if proj == nil {
		return nil
	}
	return a.toProjectInfo(proj)
}

// GetProjectByPath returns project info by path.
func (a *ProjectSourceAdapter) GetProjectByPath(path string) *comms.ProjectInfo {
	proj := a.cfg.GetProject(path)
	if proj == nil {
		return nil
	}
	return a.toProjectInfo(proj)
}

// GetDefaultProject returns the default project info.
func (a *ProjectSourceAdapter) GetDefaultProject() *comms.ProjectInfo {
	proj := a.cfg.GetDefaultProject()
	if proj == nil {
		return nil
	}
	return a.toProjectInfo(proj)
}

// ListProjects returns all configured projects.
func (a *ProjectSourceAdapter) ListProjects() []*comms.ProjectInfo {
	result := make([]*comms.ProjectInfo, 0, len(a.cfg.Projects))
	for _, proj := range a.cfg.Projects {
		result = append(result, a.toProjectInfo(proj))
	}
	return result
}

// toProjectInfo converts ProjectConfig to comms.ProjectInfo.
func (a *ProjectSourceAdapter) toProjectInfo(proj *ProjectConfig) *comms.ProjectInfo {
	return &comms.ProjectInfo{
		Name:          proj.Name,
		Path:          proj.Path,
		Navigator:     proj.Navigator,
		DefaultBranch: proj.DefaultBranch,
	}
}
