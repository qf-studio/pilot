package executor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeAllowlist is a hand-rolled RepoAllowlist for tests so we don't pull in
// the real config types. Each entry maps "owner/repo" → projectPath.
type fakeAllowlist struct {
	repos map[string]string // ownerRepo → projectPath
}

func (f *fakeAllowlist) RepoIsAllowed(owner, repo, projectPath string) bool {
	want, ok := f.repos[owner+"/"+repo]
	if !ok {
		return false
	}
	if projectPath == "" {
		return true
	}
	return want == projectPath
}

func (f *fakeAllowlist) ConfiguredRepos() []string {
	out := make([]string, 0, len(f.repos))
	for k := range f.repos {
		out = append(out, k)
	}
	return out
}

func TestParseGitHubRemoteURL(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{"https with .git", "https://github.com/qf-studio/pilot.git", "qf-studio", "pilot", false},
		{"https without .git", "https://github.com/qf-studio/pilot", "qf-studio", "pilot", false},
		{"https with trailing slash", "https://github.com/qf-studio/pilot/", "qf-studio", "pilot", false},
		{"ssh scp-like with .git", "git@github.com:qf-studio/pilot.git", "qf-studio", "pilot", false},
		{"ssh scp-like without .git", "git@github.com:qf-studio/pilot", "qf-studio", "pilot", false},
		{"ssh url form", "ssh://git@github.com/qf-studio/pilot.git", "qf-studio", "pilot", false},
		{"http insecure", "http://github.com/qf-studio/pilot.git", "qf-studio", "pilot", false},
		{"enterprise host", "https://github.enterprise.example.com/team/svc.git", "team", "svc", false},
		{"bare owner/repo", "qf-studio/pilot", "qf-studio", "pilot", false},
		{"empty url", "", "", "", true},
		{"missing repo", "https://github.com/qf-studio", "", "", true},
		{"trailing slash only", "https://github.com/qf-studio/", "", "", true},
		{"scp form missing repo", "git@github.com:qf-studio", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo, err := parseGitHubRemoteURL(tt.url)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseGitHubRemoteURL(%q) want error, got owner=%q repo=%q", tt.url, owner, repo)
				}
				if !errors.Is(err, ErrNoOriginRemote) {
					t.Fatalf("error %v should wrap ErrNoOriginRemote", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseGitHubRemoteURL(%q) unexpected error: %v", tt.url, err)
			}
			if owner != tt.wantOwner || repo != tt.wantRepo {
				t.Errorf("parseGitHubRemoteURL(%q) = (%q,%q), want (%q,%q)",
					tt.url, owner, repo, tt.wantOwner, tt.wantRepo)
			}
		})
	}
}

func TestValidateTargetRepo(t *testing.T) {
	// Ensure bypass env var is clear for tests that don't set it.
	t.Setenv(envBypassRepoAllowlist, "")

	allow := &fakeAllowlist{repos: map[string]string{
		"qf-studio/pilot": "/Users/me/projects/pilot",
		"alice/site":      "/Users/me/projects/site",
	}}

	tests := []struct {
		name        string
		allow       RepoAllowlist
		owner       string
		repo        string
		projectPath string
		bypass      string
		wantErr     error // nil = success, otherwise sentinel to match with errors.Is
	}{
		{
			name:        "happy_path_repo_and_projectPath_match",
			allow:       allow,
			owner:       "qf-studio",
			repo:        "pilot",
			projectPath: "/Users/me/projects/pilot",
		},
		{
			name:        "happy_path_repo_match_empty_projectPath",
			allow:       allow,
			owner:       "qf-studio",
			repo:        "pilot",
			projectPath: "",
		},
		{
			name:        "reject_unconfigured_repo",
			allow:       allow,
			owner:       "tenlisboa",
			repo:        "pilot-fork",
			projectPath: "/tmp/whatever",
			wantErr:     ErrRepoNotInConfig,
		},
		{
			name:        "reject_repo_match_but_projectPath_mismatch",
			allow:       allow,
			owner:       "qf-studio",
			repo:        "pilot",
			projectPath: "/Users/me/projects/site", // configured for alice/site, not qf-studio/pilot
			wantErr:     ErrRepoNotInConfig,
		},
		{
			name:    "reject_empty_owner",
			allow:   allow,
			owner:   "",
			repo:    "pilot",
			wantErr: ErrRepoNotInConfig,
		},
		{
			name:    "reject_empty_repo",
			allow:   allow,
			owner:   "qf-studio",
			repo:    "",
			wantErr: ErrRepoNotInConfig,
		},
		{
			name:    "reject_nil_allowlist_no_bypass",
			allow:   nil,
			owner:   "qf-studio",
			repo:    "pilot",
			wantErr: ErrRepoNotInConfig,
		},
		{
			name:   "bypass_via_env_var_with_allowlist",
			allow:  allow,
			owner:  "tenlisboa",
			repo:   "pilot-fork",
			bypass: "1",
		},
		{
			name:   "bypass_via_env_var_with_nil_allowlist",
			allow:  nil,
			owner:  "qf-studio",
			repo:   "pilot",
			bypass: "1",
		},
		{
			name:    "bypass_set_to_0_is_NOT_a_bypass",
			allow:   allow,
			owner:   "tenlisboa",
			repo:    "pilot-fork",
			bypass:  "0",
			wantErr: ErrRepoNotInConfig,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(envBypassRepoAllowlist, tt.bypass)
			err := ValidateTargetRepo(tt.allow, tt.owner, tt.repo, tt.projectPath)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("ValidateTargetRepo unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateTargetRepo want error wrapping %v, got nil", tt.wantErr)
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ValidateTargetRepo error %v does not wrap %v", err, tt.wantErr)
			}
		})
	}
}

// TestResolveGitRemote exercises the actual git invocation end-to-end. We
// initialize a throwaway repo in a temp dir and configure an origin remote.
// Skipped if git is not on PATH (highly unlikely in CI, but defensive).
func TestResolveGitRemote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	tests := []struct {
		name      string
		remoteURL string
		wantOwner string
		wantRepo  string
	}{
		{"https", "https://github.com/qf-studio/pilot.git", "qf-studio", "pilot"},
		{"ssh", "git@github.com:qf-studio/pilot.git", "qf-studio", "pilot"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			runGitForGuardrail(t, dir, "init", "-q")
			runGitForGuardrail(t, dir, "remote", "add", "origin", tt.remoteURL)

			owner, repo, err := resolveGitRemote(context.Background(), dir)
			if err != nil {
				t.Fatalf("resolveGitRemote: %v", err)
			}
			if owner != tt.wantOwner || repo != tt.wantRepo {
				t.Errorf("got (%q,%q), want (%q,%q)", owner, repo, tt.wantOwner, tt.wantRepo)
			}
		})
	}

	t.Run("no_remote_returns_sentinel", func(t *testing.T) {
		dir := t.TempDir()
		runGitForGuardrail(t, dir, "init", "-q")
		_, _, err := resolveGitRemote(context.Background(), dir)
		if err == nil {
			t.Fatal("want error for dir with no origin remote, got nil")
		}
		if !errors.Is(err, ErrNoOriginRemote) {
			t.Fatalf("err %v should wrap ErrNoOriginRemote", err)
		}
	})

	t.Run("empty_dir_returns_sentinel", func(t *testing.T) {
		_, _, err := resolveGitRemote(context.Background(), "")
		if err == nil {
			t.Fatal("want error for empty dir, got nil")
		}
		if !errors.Is(err, ErrNoOriginRemote) {
			t.Fatalf("err %v should wrap ErrNoOriginRemote", err)
		}
	})

	t.Run("nonexistent_dir_returns_sentinel", func(t *testing.T) {
		_, _, err := resolveGitRemote(context.Background(), filepath.Join(os.TempDir(), "definitely-does-not-exist-pilot-test"))
		if err == nil {
			t.Fatal("want error for missing dir, got nil")
		}
		if !errors.Is(err, ErrNoOriginRemote) {
			t.Fatalf("err %v should wrap ErrNoOriginRemote", err)
		}
		// Sanity: the underlying git error mentions the dir.
		if !strings.Contains(err.Error(), "git") {
			t.Errorf("error message should mention git: %v", err)
		}
	})
}

// runGitForGuardrail is the test helper used by repo_guardrail_test.go to
// shell out git. Named distinctly from runGit in runner_git_test.go to avoid
// a duplicate-declaration build error within the package.
func runGitForGuardrail(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		// Suppress system/global gitconfig so the test is hermetic.
		"GIT_CONFIG_NOSYSTEM=1",
		"HOME="+t.TempDir(),
		"XDG_CONFIG_HOME="+t.TempDir(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
