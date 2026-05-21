package main

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/qf-studio/pilot/internal/config"
)

// TestRepoIsAllowed_WorktreeAcceptance verifies GH-3050 follow-up: a git
// worktree of a configured project must be accepted by the allowlist.
// Reproduces the production failure path where Pilot creates ephemeral
// worktrees under /tmp/pilot-worktree-* and the strict path-equality
// check rejected every task before this fix.
func TestRepoIsAllowed_WorktreeAcceptance(t *testing.T) {
	tmp := t.TempDir()
	mainRepo := filepath.Join(tmp, "main")
	worktree := filepath.Join(tmp, "worktree")
	unrelated := filepath.Join(tmp, "unrelated-clone")

	mustGit(t, "", "init", "-q", mainRepo)
	mustGit(t, mainRepo, "commit", "--allow-empty", "-m", "init", "-q")
	mustGit(t, mainRepo, "worktree", "add", "--detach", "-q", worktree, "HEAD")

	// Unrelated clone of the same upstream URL — must NOT be allowed.
	mustGit(t, "", "init", "-q", unrelated)
	mustGit(t, unrelated, "commit", "--allow-empty", "-m", "init", "-q")

	allow := &configRepoAllowlist{cfg: &config.Config{
		Projects: []*config.ProjectConfig{{
			Name: "demo",
			Path: mainRepo,
			GitHub: &config.ProjectGitHubConfig{
				Owner: "octo",
				Repo:  "demo",
			},
		}},
	}}

	tests := []struct {
		name        string
		projectPath string
		want        bool
	}{
		{"empty path", "", true},
		{"configured path", mainRepo, true},
		{"worktree of configured path", worktree, true},
		{"unrelated repo at different path", unrelated, false},
		{"nonexistent path", filepath.Join(tmp, "ghost"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := allow.RepoIsAllowed("octo", "demo", tt.projectPath)
			if got != tt.want {
				t.Errorf("RepoIsAllowed(octo, demo, %q) = %v, want %v", tt.projectPath, got, tt.want)
			}
		})
	}
}

// TestRepoIsAllowed_RepoMismatchAlwaysRejected ensures the worktree check
// does not weaken the owner/repo guard: a worktree of project A must not
// be accepted as project B.
func TestRepoIsAllowed_RepoMismatchAlwaysRejected(t *testing.T) {
	tmp := t.TempDir()
	mainRepo := filepath.Join(tmp, "main")
	mustGit(t, "", "init", "-q", mainRepo)
	mustGit(t, mainRepo, "commit", "--allow-empty", "-m", "init", "-q")

	allow := &configRepoAllowlist{cfg: &config.Config{
		Projects: []*config.ProjectConfig{{
			Name: "demo",
			Path: mainRepo,
			GitHub: &config.ProjectGitHubConfig{
				Owner: "octo",
				Repo:  "demo",
			},
		}},
	}}

	if allow.RepoIsAllowed("other-owner", "other-repo", mainRepo) {
		t.Fatal("allowlist must reject mismatched owner/repo even at configured path")
	}
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}
