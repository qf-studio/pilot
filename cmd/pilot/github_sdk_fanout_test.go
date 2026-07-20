package main

import (
	"testing"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/config"
)

func ghProjWithBoard(name, owner, repo, path string, pb *github.ProjectBoardConfig) *config.ProjectConfig {
	return &config.ProjectConfig{
		Name:   name,
		Path:   path,
		GitHub: &config.ProjectGitHubConfig{Owner: owner, Repo: repo, ProjectBoard: pb},
	}
}

func ghProj(name, owner, repo, path string) *config.ProjectConfig {
	return &config.ProjectConfig{
		Name:   name,
		Path:   path,
		GitHub: &config.ProjectGitHubConfig{Owner: owner, Repo: repo},
	}
}

// M7 4d.2b: the fan-out drives the default adapter repo plus every projects[]
// github entry, config order, de-duplicated, with per-project path fallback and
// only the default repo flagged isDefault (board wiring).
func TestGithubSDKPollerTargets(t *testing.T) {
	tests := []struct {
		name        string
		cfg         *config.Config
		defaultPath string
		want        []githubSDKPollerTarget
	}{
		{
			name:        "default repo only",
			cfg:         &config.Config{Adapters: &config.AdaptersConfig{GitHub: &github.Config{Repo: "acme/default"}}},
			defaultPath: "/def",
			want:        []githubSDKPollerTarget{{repoFullName: "acme/default", projectPath: "/def", isDefault: true}},
		},
		{
			name: "default plus projects in config order, path fallback",
			cfg: &config.Config{
				Adapters: &config.AdaptersConfig{GitHub: &github.Config{Repo: "acme/default"}},
				Projects: []*config.ProjectConfig{ghProj("svc", "acme", "svc", "/svc"), ghProj("web", "acme", "web", "")},
			},
			defaultPath: "/def",
			want: []githubSDKPollerTarget{
				{repoFullName: "acme/default", projectPath: "/def", isDefault: true},
				{repoFullName: "acme/svc", projectPath: "/svc", isDefault: false},
				{repoFullName: "acme/web", projectPath: "/def", isDefault: false}, // no Path → default
			},
		},
		{
			name: "project duplicating the default repo is de-duped",
			cfg: &config.Config{
				Adapters: &config.AdaptersConfig{GitHub: &github.Config{Repo: "acme/default"}},
				Projects: []*config.ProjectConfig{ghProj("dup", "acme", "default", "/dup"), ghProj("svc", "acme", "svc", "/svc")},
			},
			defaultPath: "/def",
			want: []githubSDKPollerTarget{
				{repoFullName: "acme/default", projectPath: "/def", isDefault: true},
				{repoFullName: "acme/svc", projectPath: "/svc", isDefault: false},
			},
		},
		{
			name: "projects without github config are skipped",
			cfg: &config.Config{
				Adapters: &config.AdaptersConfig{GitHub: &github.Config{Repo: "acme/default"}},
				Projects: []*config.ProjectConfig{{Name: "nogh", Path: "/nogh"}, ghProj("svc", "acme", "svc", "/svc")},
			},
			defaultPath: "/def",
			want: []githubSDKPollerTarget{
				{repoFullName: "acme/default", projectPath: "/def", isDefault: true},
				{repoFullName: "acme/svc", projectPath: "/svc", isDefault: false},
			},
		},
		{
			name: "no default repo yields only projects, none isDefault",
			cfg: &config.Config{
				Adapters: &config.AdaptersConfig{GitHub: &github.Config{}},
				Projects: []*config.ProjectConfig{ghProj("svc", "acme", "svc", "/svc")},
			},
			defaultPath: "/def",
			want: []githubSDKPollerTarget{
				{repoFullName: "acme/svc", projectPath: "/svc", isDefault: false},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := githubSDKPollerTargets(tt.cfg, tt.defaultPath)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d targets, want %d: %+v", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("target[%d] = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestResolveSDKProjectBoard covers GH-4472 poller wiring: sdkCfg.ProjectBoard
// is resolved per-target via Config.ResolveProjectBoard, not gated on
// target.isDefault the way the pre-GH-4472 code hard-coded it.
func TestResolveSDKProjectBoard(t *testing.T) {
	globalBoard := &github.ProjectBoardConfig{Enabled: true, ProjectNumber: 2, StatusField: "Status"}
	projectBoard := &github.ProjectBoardConfig{Enabled: true, ProjectNumber: 1, StatusField: "Status", SourceEnabled: true, SourceStatus: "Todo"}

	cfg := &config.Config{
		Adapters: &config.AdaptersConfig{
			GitHub: &github.Config{Repo: "acme/default", ProjectBoard: globalBoard},
		},
		Projects: []*config.ProjectConfig{
			ghProjWithBoard("with-board", "acme", "with-board", "/wb", projectBoard),
			ghProj("no-board", "acme", "no-board", "/nb"),
		},
	}

	tests := []struct {
		name   string
		target githubSDKPollerTarget
		want   *github.ProjectBoardConfig // nil means expect sdkCfg.ProjectBoard == nil
	}{
		{
			name:   "non-default target with its own project board gets it wired",
			target: githubSDKPollerTarget{repoFullName: "acme/with-board", isDefault: false},
			want:   projectBoard,
		},
		{
			name:   "non-default target without a project board stays nil (no fallback to global)",
			target: githubSDKPollerTarget{repoFullName: "acme/no-board", isDefault: false},
			want:   nil,
		},
		{
			name:   "default target with no project-level override keeps the global fallback",
			target: githubSDKPollerTarget{repoFullName: "acme/default", isDefault: true},
			want:   globalBoard,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveSDKProjectBoard(cfg, tt.target)
			if tt.want == nil {
				if got != nil {
					t.Fatalf("resolveSDKProjectBoard() = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("resolveSDKProjectBoard() = nil, want non-nil")
			}
			if got.ProjectNumber != tt.want.ProjectNumber || got.SourceEnabled != tt.want.SourceEnabled || got.SourceStatus != tt.want.SourceStatus {
				t.Errorf("resolveSDKProjectBoard() = %+v, want mapped from %+v", got, tt.want)
			}
		})
	}
}

// M7 4d.2c: the issue URL must use the resolved owner/repo (project repos are not
// the default adapter repo) and fall back to the default repo, then to empty.
func TestGithubIssueURL(t *testing.T) {
	cfg := &config.Config{Adapters: &config.AdaptersConfig{GitHub: &github.Config{Repo: "acme/default"}}}

	if got := githubIssueURL(cfg, "acme", "svc", "42"); got != "https://github.com/acme/svc/issues/42" {
		t.Errorf("explicit owner/repo: got %q", got)
	}
	if got := githubIssueURL(cfg, "", "", "42"); got != "https://github.com/acme/default/issues/42" {
		t.Errorf("default fallback: got %q", got)
	}
	emptyCfg := &config.Config{Adapters: &config.AdaptersConfig{GitHub: &github.Config{}}}
	if got := githubIssueURL(emptyCfg, "", "", "42"); got != "" {
		t.Errorf("no repo anywhere: got %q, want empty", got)
	}
}
