package main

import (
	"testing"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/memory"
)

func TestApprovalRepoResolver(t *testing.T) {
	cfg := &config.Config{
		Adapters: &config.AdaptersConfig{GitHub: &github.Config{Repo: "OrgA/RepoA"}},
		Projects: []*config.ProjectConfig{
			{Name: "b", Path: "/work/repo-b", GitHub: &config.ProjectGitHubConfig{Owner: "OrgA", Repo: "RepoB"}},
			{Name: "nogh", Path: "/work/no-github"},
		},
	}
	resolve := approvalRepoResolver(cfg, "/work/repo-a")

	tests := []struct {
		name        string
		project     string
		owner, repo string
		ok          bool
	}{
		{"project with github block", memory.CanonicalizeProjectPath("/work/repo-b"), "OrgA", "RepoB", true},
		{"daemon project path maps to default repo", memory.CanonicalizeProjectPath("/work/repo-a"), "OrgA", "RepoA", true},
		{"project without github block fails closed", memory.CanonicalizeProjectPath("/work/no-github"), "", "", false},
		{"unknown project fails closed", "/work/unknown", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo, ok := resolve(tt.project)
			if owner != tt.owner || repo != tt.repo || ok != tt.ok {
				t.Errorf("resolve(%q) = %q, %q, %v; want %q, %q, %v", tt.project, owner, repo, ok, tt.owner, tt.repo, tt.ok)
			}
		})
	}
}
