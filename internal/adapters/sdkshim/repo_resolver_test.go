package sdkshim

import (
	"errors"
	"testing"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/studio-sdk/sdk/core"
)

func TestResolveRepoForEvent_NilConfig(t *testing.T) {
	_, _, _, err := ResolveRepoForEvent(nil, "plane", core.IssueEvent{})
	if err == nil {
		t.Fatal("expected error for nil config, got nil")
	}
}

func TestResolveRepoForEvent_Stub(t *testing.T) {
	// Not-yet-implemented sources (plane/gitlab/azuredevops/linear/jira/asana) still
	// return ErrRepoNotResolved. Only github is implemented (Phase 4).
	cfg := &config.Config{}
	_, _, _, err := ResolveRepoForEvent(cfg, "plane", core.IssueEvent{ProjectID: "abc-123"})
	if !errors.Is(err, ErrRepoNotResolved) {
		t.Fatalf("expected ErrRepoNotResolved, got %v", err)
	}
}

// TestResolveRepoForEvent_Github covers the Phase-4 github branch: per-project routing by
// repo name (ev.ProjectID is the repo name per the SDK adapter), default-adapter fallback,
// and the unresolved case. ev.ProjectID carries the repo NAME, not "owner/repo".
func TestResolveRepoForEvent_Github(t *testing.T) {
	cfg := &config.Config{
		Adapters: &config.AdaptersConfig{
			GitHub: &github.Config{Repo: "qf-studio/pilot"},
		},
		Projects: []*config.ProjectConfig{
			{GitHub: &config.ProjectGitHubConfig{Owner: "acme", Repo: "widget"}},
		},
	}

	tests := []struct {
		name      string
		projectID string // ev.ProjectID == repo name
		wantOwner string
		wantRepo  string
		wantClone string
	}{
		{
			name:      "per-project match by repo name",
			projectID: "widget",
			wantOwner: "acme",
			wantRepo:  "widget",
			wantClone: "https://github.com/acme/widget.git",
		},
		{
			name:      "fallback to default adapter repo on no project match",
			projectID: "some-other-repo",
			wantOwner: "qf-studio",
			wantRepo:  "pilot",
			wantClone: "https://github.com/qf-studio/pilot.git",
		},
		{
			name:      "empty projectID falls back to default adapter repo",
			projectID: "",
			wantOwner: "qf-studio",
			wantRepo:  "pilot",
			wantClone: "https://github.com/qf-studio/pilot.git",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clone, owner, repo, err := ResolveRepoForEvent(cfg, "github", core.IssueEvent{ProjectID: tt.projectID})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if owner != tt.wantOwner || repo != tt.wantRepo || clone != tt.wantClone {
				t.Errorf("got (%q,%q,%q), want (%q,%q,%q)", clone, owner, repo, tt.wantClone, tt.wantOwner, tt.wantRepo)
			}
		})
	}
}

// TestResolveRepoForEvent_GithubUnresolved verifies ErrRepoNotResolved when neither a
// configured project nor a default adapter repo is available (and that a nil Adapters
// pointer does not panic).
func TestResolveRepoForEvent_GithubUnresolved(t *testing.T) {
	cfg := &config.Config{} // no Adapters, no Projects
	_, _, _, err := ResolveRepoForEvent(cfg, "github", core.IssueEvent{ProjectID: "anything"})
	if !errors.Is(err, ErrRepoNotResolved) {
		t.Fatalf("expected ErrRepoNotResolved, got %v", err)
	}
}
