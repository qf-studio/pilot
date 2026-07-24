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

// checkGitClean verifies the git working directory has no uncommitted changes.
// This prevents execution from accidentally including unrelated changes.
//
// GH-4526: on hosted tenants the daemon scaffolds Navigator's `.agent/`
// directory into freshly-cloned repos a couple minutes after clone
// (NavigatorInitializer.Initialize) — those repos don't track `.agent/` yet
// (box repos do, having committed it years ago, so this never surfaced
// there). That scaffold write leaves an untracked `.agent/` in every fresh
// hosted clone, which this check then reports as "dirty", permanently
// blocking the very first dispatch on every hosted repo. The scaffold is the
// daemon's own bookkeeping, not user work, so untracked paths under
// `.agent/` are excluded from the dirty count here. Changes to an already
// *tracked* file under `.agent/` (e.g. a box repo's committed graph.json)
// still count as dirty — this does not weaken the check for genuine user
// changes.
func checkGitClean(ctx context.Context, projectPath string) error {
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	cmd.Dir = projectPath
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("git status failed: %w", err)
	}
	changes := strings.TrimSpace(string(output))
	if len(changes) == 0 {
		return nil
	}

	var dirty []string
	for _, line := range strings.Split(changes, "\n") {
		if isScaffoldNoise(line) {
			continue
		}
		dirty = append(dirty, line)
	}

	if len(dirty) > 0 {
		return fmt.Errorf("working directory has %d uncommitted change(s): run 'git stash' or 'git commit' first", len(dirty))
	}
	return nil
}

// isScaffoldNoise reports whether a `git status --porcelain` line represents
// an untracked path created by the daemon's own Navigator scaffold
// (NavigatorInitializer.Initialize, internal/executor/navigator.go) rather
// than a genuine user change. Only untracked ("??") entries are eligible —
// a modified/staged file under `.agent/` (which box repos commit) is still
// real dirty state and must keep failing this check. GH-4526.
func isScaffoldNoise(line string) bool {
	if len(line) < 3 || !strings.HasPrefix(line, "??") {
		return false
	}
	path := strings.TrimSpace(line[2:])
	path = strings.Trim(path, `"`)
	return path == ".agent" || strings.HasPrefix(path, ".agent/")
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
