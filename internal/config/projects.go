package config

import (
	"github.com/alekspetrov/pilot/internal/adapters/slack"
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

// SlackProjectSourceAdapter wraps Config to implement slack.ProjectSource (GH-652)
type SlackProjectSourceAdapter struct {
	cfg *Config
}

// NewSlackProjectSource creates a new slack.ProjectSource from Config
func NewSlackProjectSource(cfg *Config) slack.ProjectSource {
	if cfg == nil {
		return nil
	}
	return &SlackProjectSourceAdapter{cfg: cfg}
}

// GetProjectByName returns project info by name
func (a *SlackProjectSourceAdapter) GetProjectByName(name string) *slack.ProjectInfo {
	proj := a.cfg.GetProjectByName(name)
	if proj == nil {
		return nil
	}
	return a.toSlackProjectInfo(proj)
}

// GetProjectByPath returns project info by path
func (a *SlackProjectSourceAdapter) GetProjectByPath(path string) *slack.ProjectInfo {
	proj := a.cfg.GetProject(path)
	if proj == nil {
		return nil
	}
	return a.toSlackProjectInfo(proj)
}

// GetDefaultProject returns the default project info
func (a *SlackProjectSourceAdapter) GetDefaultProject() *slack.ProjectInfo {
	proj := a.cfg.GetDefaultProject()
	if proj == nil {
		return nil
	}
	return a.toSlackProjectInfo(proj)
}

// ListProjects returns all configured projects
func (a *SlackProjectSourceAdapter) ListProjects() []*slack.ProjectInfo {
	result := make([]*slack.ProjectInfo, 0, len(a.cfg.Projects))
	for _, proj := range a.cfg.Projects {
		result = append(result, a.toSlackProjectInfo(proj))
	}
	return result
}

// toSlackProjectInfo converts ProjectConfig to slack.ProjectInfo
func (a *SlackProjectSourceAdapter) toSlackProjectInfo(proj *ProjectConfig) *slack.ProjectInfo {
	return &slack.ProjectInfo{
		Name:          proj.Name,
		Path:          proj.Path,
		Navigator:     proj.Navigator,
		DefaultBranch: proj.DefaultBranch,
	}
}
