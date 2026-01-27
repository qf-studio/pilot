package config

import (
	"github.com/alekspetrov/pilot/internal/adapters/telegram"
)

// ProjectSourceAdapter wraps Config to implement telegram.ProjectSource
type ProjectSourceAdapter struct {
	cfg *Config
}

// NewProjectSource creates a new telegram.ProjectSource from Config
func NewProjectSource(cfg *Config) telegram.ProjectSource {
	if cfg == nil {
		return nil
	}
	return &ProjectSourceAdapter{cfg: cfg}
}

// GetProjectByName returns project info by name
func (a *ProjectSourceAdapter) GetProjectByName(name string) *telegram.ProjectInfo {
	proj := a.cfg.GetProjectByName(name)
	if proj == nil {
		return nil
	}
	return a.toProjectInfo(proj)
}

// GetProjectByPath returns project info by path
func (a *ProjectSourceAdapter) GetProjectByPath(path string) *telegram.ProjectInfo {
	proj := a.cfg.GetProject(path)
	if proj == nil {
		return nil
	}
	return a.toProjectInfo(proj)
}

// GetDefaultProject returns the default project info
func (a *ProjectSourceAdapter) GetDefaultProject() *telegram.ProjectInfo {
	proj := a.cfg.GetDefaultProject()
	if proj == nil {
		return nil
	}
	return a.toProjectInfo(proj)
}

// ListProjects returns all configured projects
func (a *ProjectSourceAdapter) ListProjects() []*telegram.ProjectInfo {
	result := make([]*telegram.ProjectInfo, 0, len(a.cfg.Projects))
	for _, proj := range a.cfg.Projects {
		result = append(result, a.toProjectInfo(proj))
	}
	return result
}

// toProjectInfo converts ProjectConfig to telegram.ProjectInfo
func (a *ProjectSourceAdapter) toProjectInfo(proj *ProjectConfig) *telegram.ProjectInfo {
	return &telegram.ProjectInfo{
		Name:          proj.Name,
		Path:          proj.Path,
		Navigator:     proj.Navigator,
		DefaultBranch: proj.DefaultBranch,
	}
}
