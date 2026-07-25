package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// PreflightCheck represents a single pre-execution health check.
// GH-915: Pre-flight checks catch environmental issues early before wasting time on execution.
type PreflightCheck struct {
	Name        string
	Description string
	Check       func(ctx context.Context, projectPath string) error
}

// DefaultPreflightChecks returns the standard set of pre-flight checks.
var DefaultPreflightChecks = []PreflightCheck{
	{
		Name:        "claude_available",
		Description: "Verify Claude Code CLI is available",
		Check:       checkClaudeAvailable,
	},
	{
		Name:        "git_clean",
		Description: "Verify git working directory is clean",
		Check:       checkGitClean,
	},
	{
		Name:        "git_repo",
		Description: "Verify directory is a git repository",
		Check:       checkGitRepo,
	},
}

// PreflightOptions configures which pre-flight checks to run.
type PreflightOptions struct {
	// SkipGitClean skips the git_clean check. Use this when worktree isolation
	// is enabled, as the worktree is always clean (created from a commit).
	SkipGitClean bool

	// BackendType specifies the configured backend ("claude-code", "opencode", "qwen-code").
	// When set, the CLI availability check matches the active backend instead of
	// always requiring 'claude'.
	BackendType string
}

// RunPreflightChecks executes all default pre-flight checks.
// Returns the first error encountered, or nil if all checks pass.
func RunPreflightChecks(ctx context.Context, projectPath string) error {
	return RunPreflightChecksCustom(ctx, projectPath, DefaultPreflightChecks)
}

// RunPreflightChecksWithOptions executes pre-flight checks with the given options.
// GH-1002: When worktree isolation is enabled, the git_clean check is skipped
// because worktrees are always created from a commit (clean state).
func RunPreflightChecksWithOptions(ctx context.Context, projectPath string, opts PreflightOptions) error {
	checks := DefaultPreflightChecks

	// GH-1483: Replace hardcoded claude check with backend-aware check
	if opts.BackendType != "" && opts.BackendType != "claude-code" {
		var filtered []PreflightCheck
		for _, c := range checks {
			if c.Name == "claude_available" {
				if opts.BackendType == BackendTypeOpenAIAPI || opts.BackendType == BackendTypeAnthropicAPI {
					// Direct HTTP backends have no CLI — verify API key is present instead
					filtered = append(filtered, PreflightCheck{
						Name:        "api_key_configured",
						Description: fmt.Sprintf("Verify API key is configured for %s backend", opts.BackendType),
						Check: func(ctx context.Context, _ string) error {
							return checkOpenAIAPIKey(opts.BackendType)
						},
					})
				} else {
					filtered = append(filtered, PreflightCheck{
						Name:        "backend_available",
						Description: fmt.Sprintf("Verify %s CLI is available", opts.BackendType),
						Check: func(ctx context.Context, _ string) error {
							return checkBackendCLI(ctx, opts.BackendType)
						},
					})
				}
			} else {
				filtered = append(filtered, c)
			}
		}
		checks = filtered
	}

	if opts.SkipGitClean {
		var filtered []PreflightCheck
		for _, c := range checks {
			if c.Name != "git_clean" {
				filtered = append(filtered, c)
			}
		}
		checks = filtered
	}

	return RunPreflightChecksCustom(ctx, projectPath, checks)
}

// getChecksWithoutGitClean returns the default checks minus the git_clean check.
func getChecksWithoutGitClean() []PreflightCheck {
	var result []PreflightCheck
	for _, check := range DefaultPreflightChecks {
		if check.Name != "git_clean" {
			result = append(result, check)
		}
	}
	return result
}

// RunPreflightChecksCustom executes a custom set of pre-flight checks.
func RunPreflightChecksCustom(ctx context.Context, projectPath string, checks []PreflightCheck) error {
	for _, check := range checks {
		if err := check.Check(ctx, projectPath); err != nil {
			return &PreflightError{
				CheckName: check.Name,
				Err:       err,
			}
		}
	}
	return nil
}

// PreflightError represents a failed pre-flight check.
type PreflightError struct {
	CheckName string
	Err       error
}

func (e *PreflightError) Error() string {
	return fmt.Sprintf("preflight check %q failed: %v", e.CheckName, e.Err)
}

func (e *PreflightError) Unwrap() error {
	return e.Err
}

// checkClaudeAvailable verifies the claude CLI is installed and accessible.
func checkClaudeAvailable(ctx context.Context, _ string) error {
	return checkBackendCLI(ctx, "claude-code")
}

// backendCLICommands maps backend type to the CLI command and version flag.
var backendCLICommands = map[string]struct {
	command     string
	versionFlag string
}{
	"claude-code": {command: "claude", versionFlag: "--version"},
	"opencode":    {command: "opencode", versionFlag: "version"},
	"qwen-code":   {command: "qwen", versionFlag: "--version"},
}

// checkBackendCLI verifies the CLI for the given backend type is available.
func checkBackendCLI(ctx context.Context, backendType string) error {
	info, ok := backendCLICommands[backendType]
	if !ok {
		// Unknown backend — skip check rather than block
		return nil
	}
	cmd := exec.CommandContext(ctx, info.command, info.versionFlag)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s command not available: %w (output: %s)", info.command, err, strings.TrimSpace(string(output)))
	}
	return nil
}

// checkOpenAIAPIKey verifies that an API key is available for direct HTTP backends
// (openai-api, anthropic-api). No network call — local env check only.
func checkOpenAIAPIKey(backendType string) error {
	keys := []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "PILOT_ENGINE_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"}
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return nil
		}
	}
	return fmt.Errorf("%s backend requires an API key: set OPENAI_API_KEY (or configure executor.openai.api_key)", backendType)
}

// checkGitClean verifies the git working directory has no uncommitted changes
// outside of Pilot's own scaffold/excluded paths.
//
// GH-4526: on a hosted tenant repo, NavigatorInitializer.Initialize
// (navigator.go) auto-scaffolds an untracked .agent/ structure into the
// fresh clone before the first dispatch ever runs (box repos never hit this
// because .agent/ is already committed there). This check's own definition
// of "dirty" then blocks every dispatch attempt on that self-inflicted
// scaffold — 5 re-picks, repick hard cap, pilot-blocked, on a repo whose only
// "uncommitted change" is Pilot's own bootstrapping, not the user's code.
// Reuse the exact defaultExcludeDirs/defaultExcludeGlobs list git.go's
// Commit() step already uses to decide what it will never auto-stage
// (.agent/, .claude/, node_modules/, build artifacts, lock files, ...): a
// path the executor already treats as "not really a project change" for
// commit purposes is, by the same reasoning, not evidence of a dirty working
// tree for preflight purposes either. Genuinely dirty user files (anything
// NOT matching that exclude list) still block exactly as before.
func checkGitClean(ctx context.Context, projectPath string) error {
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain", "-z")
	cmd.Dir = projectPath
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("git status failed: %w", err)
	}

	trimmed := strings.TrimRight(string(output), "\x00")
	if trimmed == "" {
		return nil
	}

	var dirty []string
	for _, entry := range strings.Split(trimmed, "\x00") {
		if len(entry) < 4 {
			continue
		}
		path := entry[3:]
		if isExcluded(path) {
			continue
		}
		dirty = append(dirty, path)
	}

	if len(dirty) > 0 {
		return fmt.Errorf("working directory has %d uncommitted change(s): run 'git stash' or 'git commit' first", len(dirty))
	}
	return nil
}

// checkGitRepo verifies the directory is a valid git repository.
func checkGitRepo(ctx context.Context, projectPath string) error {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--git-dir")
	cmd.Dir = projectPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("not a git repository: %w (output: %s)", err, strings.TrimSpace(string(output)))
	}
	return nil
}
