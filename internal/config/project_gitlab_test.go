package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func gitlabProjectCfg(token string, projects ...*ProjectConfig) *Config {
	c := DefaultConfig()
	c.Adapters.GitLab.Token = token
	c.Projects = projects
	return c
}

// TestConfig_Validate_ProjectGitLab covers the GH-5583 validation rules.
func TestConfig_Validate_ProjectGitLab(t *testing.T) {
	tests := []struct {
		name        string
		cfg         *Config
		errContains string // "" = valid
	}{
		{
			name: "valid",
			cfg: gitlabProjectCfg("test-gitlab-token",
				&ProjectConfig{Name: "service", Path: "/srv/service", GitLab: &ProjectGitLabConfig{Project: "example-group/service"}}),
		},
		{
			name: "valid subgroup path with base_url",
			cfg: gitlabProjectCfg("test-gitlab-token",
				&ProjectConfig{Name: "client", Path: "/srv/client", GitLab: &ProjectGitLabConfig{Project: "grp/sub/client", BaseURL: "https://gitlab.example.com"}}),
		},
		{
			name: "no gitlab block needs no token",
			cfg:  gitlabProjectCfg("", &ProjectConfig{Name: "plain", Path: "/srv/plain"}),
		},
		{
			name: "project not namespace/path",
			cfg: gitlabProjectCfg("test-gitlab-token",
				&ProjectConfig{Name: "service", Path: "/srv/service", GitLab: &ProjectGitLabConfig{Project: "service"}}),
			errContains: "gitlab.project must be namespace/path",
		},
		{
			name: "project empty",
			cfg: gitlabProjectCfg("test-gitlab-token",
				&ProjectConfig{Name: "service", Path: "/srv/service", GitLab: &ProjectGitLabConfig{}}),
			errContains: "gitlab.project must be namespace/path",
		},
		{
			name: "project with trailing slash",
			cfg: gitlabProjectCfg("test-gitlab-token",
				&ProjectConfig{Name: "service", Path: "/srv/service", GitLab: &ProjectGitLabConfig{Project: "grp/service/"}}),
			errContains: "gitlab.project must be namespace/path",
		},
		{
			name: "both github and gitlab",
			cfg: gitlabProjectCfg("test-gitlab-token",
				&ProjectConfig{Name: "both", Path: "/srv/both",
					GitHub: &ProjectGitHubConfig{Owner: "o", Repo: "r"},
					GitLab: &ProjectGitLabConfig{Project: "grp/both"}}),
			errContains: "may not declare both a github and a gitlab block",
		},
		{
			name: "empty adapters.gitlab.token names the project",
			cfg: gitlabProjectCfg("",
				&ProjectConfig{Name: "service", Path: "/srv/service", GitLab: &ProjectGitLabConfig{Project: "example-group/service"}}),
			errContains: "(service): gitlab: block requires adapters.gitlab.token",
		},
		{
			name: "nil adapters.gitlab is an empty token",
			cfg: func() *Config {
				c := gitlabProjectCfg("test-gitlab-token",
					&ProjectConfig{Name: "service", Path: "/srv/service", GitLab: &ProjectGitLabConfig{Project: "example-group/service"}})
				c.Adapters.GitLab = nil
				return c
			}(),
			errContains: "requires adapters.gitlab.token",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.errContains == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.errContains) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tt.errContains)
			}
		})
	}
}

func TestConfig_ResolveGitLabBaseURL(t *testing.T) {
	c := DefaultConfig()
	pc := &ProjectGitLabConfig{Project: "g/p"}
	if got := c.ResolveGitLabBaseURL(pc); got != "https://gitlab.com" {
		t.Errorf("default = %q, want https://gitlab.com", got)
	}
	c.Adapters.GitLab.BaseURL = "https://adapter.example.com"
	if got := c.ResolveGitLabBaseURL(pc); got != "https://adapter.example.com" {
		t.Errorf("adapter fallback = %q", got)
	}
	pc.BaseURL = "https://project.example.com"
	if got := c.ResolveGitLabBaseURL(pc); got != "https://project.example.com" {
		t.Errorf("project override = %q", got)
	}
}

func TestProjectConfig_GitLabYAML(t *testing.T) {
	const doc = `
name: service
path: /srv/repos/service
gitlab:
  project: example-group/service
  base_url: https://gitlab.example.com
`
	var p ProjectConfig
	if err := yaml.Unmarshal([]byte(doc), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.GitLab == nil || p.GitLab.Project != "example-group/service" || p.GitLab.BaseURL != "https://gitlab.example.com" {
		t.Fatalf("GitLab block not bound: %+v", p.GitLab)
	}
}
