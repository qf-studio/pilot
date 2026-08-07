package main

import (
	"testing"

	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/config"
)

// TestProjectBoardControllerOpts covers GH-4472: the autopilot controller
// wiring sites (main.go) resolve board sync per-repo via
// projectBoardControllerOpts instead of always reading the global
// adapters.github.project_board block.
func TestProjectBoardControllerOpts(t *testing.T) {
	apGHClient := githubSDK.NewClient("fake-token")
	globalBoard := &github.ProjectBoardConfig{Enabled: true, ProjectNumber: 2}
	projectBoard := &github.ProjectBoardConfig{Enabled: true, ProjectNumber: 1}
	disabledBoard := &github.ProjectBoardConfig{Enabled: false, ProjectNumber: 3}

	cfg := &config.Config{
		Adapters: &config.AdaptersConfig{
			GitHub: &github.Config{Repo: "acme/default", ProjectBoard: globalBoard},
		},
		Projects: []*config.ProjectConfig{
			ghProjWithBoard("with-board", "acme", "with-board", "/wb", projectBoard),
			ghProjWithBoard("disabled", "acme", "disabled", "/db", disabledBoard),
			ghProj("no-board", "acme", "no-board", "/nb"),
		},
	}

	tests := []struct {
		name          string
		repoFullName  string
		owner         string
		isDefaultRepo bool
		wantOpts      int
	}{
		{
			name:          "non-default repo with its own enabled board gets an option",
			repoFullName:  "acme/with-board",
			owner:         "acme",
			isDefaultRepo: false,
			wantOpts:      1,
		},
		{
			name:          "non-default repo with a disabled board gets no option",
			repoFullName:  "acme/disabled",
			owner:         "acme",
			isDefaultRepo: false,
			wantOpts:      0,
		},
		{
			name:          "non-default repo with no board config gets no option (no fallback to global)",
			repoFullName:  "acme/no-board",
			owner:         "acme",
			isDefaultRepo: false,
			wantOpts:      0,
		},
		{
			name:          "default repo with no project-level override falls back to global",
			repoFullName:  "acme/default",
			owner:         "acme",
			isDefaultRepo: true,
			wantOpts:      1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := projectBoardControllerOpts(apGHClient, cfg, tt.repoFullName, tt.owner, tt.isDefaultRepo)
			if len(got) != tt.wantOpts {
				t.Errorf("projectBoardControllerOpts() len = %d, want %d", len(got), tt.wantOpts)
			}
		})
	}
}

// TestProjectApprovalControllerOpts_GH4774 covers GH-4774: all three
// autopilot controller construction sites (default repo, projects loop, and
// — critically, since it used to be skipped entirely — gateway mode) resolve
// the per-project approval overlay through this one shared helper, so
// testing it here stands in for exercising every call site including the
// gateway-mode one that isn't otherwise unit-testable in isolation (it's
// wired inline inside main()'s gateway startup branch).
func TestProjectApprovalControllerOpts_GH4774(t *testing.T) {
	requireApprovalFalse := false
	requireApprovalTrue := true

	cfg := &config.Config{
		Adapters: &config.AdaptersConfig{
			GitHub: &github.Config{Repo: "acme/default"},
		},
		Projects: []*config.ProjectConfig{
			{
				Name:   "personal",
				Path:   "/personal",
				GitHub: &config.ProjectGitHubConfig{Owner: "acme", Repo: "personal"},
				Approval: &autopilot.ProjectApprovalOverride{
					RequireApproval: &requireApprovalFalse,
					ApprovalSource:  autopilot.ApprovalSourceTelegram,
				},
			},
			{
				Name:   "work",
				Path:   "/work",
				GitHub: &config.ProjectGitHubConfig{Owner: "acme", Repo: "work"},
				Approval: &autopilot.ProjectApprovalOverride{
					RequireApproval: &requireApprovalTrue,
					ApprovalSource:  autopilot.ApprovalSourceSlack,
				},
			},
			ghProj("no-override", "acme", "no-override", "/no"),
		},
	}

	t.Run("repo with no projects[] entry gets no option", func(t *testing.T) {
		got := projectApprovalControllerOpts(cfg, "acme/default")
		if len(got) != 0 {
			t.Errorf("len = %d, want 0", len(got))
		}
	})

	t.Run("project with no approval override gets no option", func(t *testing.T) {
		got := projectApprovalControllerOpts(cfg, "acme/no-override")
		if len(got) != 0 {
			t.Errorf("len = %d, want 0", len(got))
		}
	})

	t.Run("personal project resolves to telegram + require_approval=false", func(t *testing.T) {
		got := projectApprovalControllerOpts(cfg, "acme/personal")
		if len(got) != 1 {
			t.Fatalf("len = %d, want 1", len(got))
		}
		c := autopilot.NewController(autopilot.DefaultConfig(), nil, nil, "acme", "personal", got...)
		if c.ResolvedRequireApproval() {
			t.Error("ResolvedRequireApproval() = true, want false (project override)")
		}
		if got := c.ResolvedApprovalSource(); got != autopilot.ApprovalSourceTelegram {
			t.Errorf("ResolvedApprovalSource() = %q, want %q", got, autopilot.ApprovalSourceTelegram)
		}
	})

	t.Run("work project resolves to slack + require_approval=true independently of the personal project", func(t *testing.T) {
		got := projectApprovalControllerOpts(cfg, "acme/work")
		if len(got) != 1 {
			t.Fatalf("len = %d, want 1", len(got))
		}
		c := autopilot.NewController(autopilot.DefaultConfig(), nil, nil, "acme", "work", got...)
		if !c.ResolvedRequireApproval() {
			t.Error("ResolvedRequireApproval() = false, want true (project override)")
		}
		if got := c.ResolvedApprovalSource(); got != autopilot.ApprovalSourceSlack {
			t.Errorf("ResolvedApprovalSource() = %q, want %q", got, autopilot.ApprovalSourceSlack)
		}
	})
}
