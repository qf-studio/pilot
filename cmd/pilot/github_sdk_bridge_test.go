package main

import (
	"testing"

	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"

	"github.com/qf-studio/pilot/internal/adapters/github"
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
