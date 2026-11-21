package executor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// LintResult contains the result of a lint check
type LintResult struct {
	Clean    bool
	Linter   string
	Issues   []string
	FixedAll bool
	// Err is a lint command that did not run (GH-6668): with run_as_user set
	// a command that is not a sudo command of the agent user
	// (lintCommandRefusedError). The caller fails the step with its text; it
	// is neither a lint failure nor a missing program.
	Err error
}

// defaultLintBaseRef is the ref lint compares against when no base branch
// resolves from the task or the repository.
const defaultLintBaseRef = "origin/main"

// lintBaseRef returns the ref golangci-lint's --new-from-rev should compare
// against: origin/<baseBranch>, or origin/main only when baseBranch is empty
// (GH-5695: a repo whose default branch is not main has no origin/main).
func lintBaseRef(baseBranch string) string {
	if baseBranch == "" {
		return defaultLintBaseRef
	}
	return "origin/" + baseBranch
}

// resolveLintBase returns baseBranch if set, else the repository's default
// branch, else "" (which lintBaseRef maps to origin/main).
func (g *GitOperations) resolveLintBase(ctx context.Context, baseBranch string) string {
	if baseBranch != "" {
		return baseBranch
	}
	if detected, err := g.GetDefaultBranch(ctx); err == nil {
		return detected
	}
	return ""
}

// goLintArgs builds the golangci-lint arguments for changed-code-only linting.
func goLintArgs(baseBranch string) []string {
	return []string{"run", "--new-from-rev=" + lintBaseRef(baseBranch), "./..."}
}

// runLintCheck detects and runs the appropriate linter for the project.
// baseBranch is the task's base branch ("" = detect from the repository).
func (g *GitOperations) runLintCheck(ctx context.Context, baseBranch string) *LintResult {
	result := &LintResult{
		Clean:  true,
		Issues: []string{},
	}

	// Detect linter config (Go projects)
	if hasGoLintConfig(g.projectPath) {
		result.Linter = "golangci-lint"
		// Check for lint issues on changed files only
		output, err := g.runGoLint(ctx, baseBranch)
		if err != nil {
			result.Clean = false
			result.Issues = []string{output}
			if refusedLintCommand(err) {
				result.Err = err
			}
		}
	}
	// Could extend with eslint, ruff, etc. in the future

	return result
}

// autoFixLint attempts to auto-fix linting issues
func (g *GitOperations) autoFixLint(ctx context.Context, baseBranch string) *LintResult {
	result := g.runLintCheck(ctx, baseBranch)
	if result.Clean || result.Err != nil {
		return result
	}

	if result.Linter == "golangci-lint" {
		// Attempt auto-fix
		output, err := g.runLintProgram(ctx, "run", "--fix", "./...")
		if refusedLintCommand(err) {
			result.Err = err
			return result
		}
		if err == nil {
			// Verify fixes worked
			verifyResult := g.runLintCheck(ctx, baseBranch)
			if verifyResult.Clean {
				// Stage and amend the last commit
				if amendErr := g.stageAndAmendCommit(ctx, "[lint fix] Auto-fixed linting issues"); amendErr != nil {
					result.Issues = []string{fmt.Sprintf("failed to amend commit with lint fixes: %v", amendErr)}
				} else {
					result.FixedAll = true
					result.Clean = true
					result.Issues = []string{}
				}
			} else {
				result.Issues = verifyResult.Issues
			}
		} else {
			result.Issues = []string{fmt.Sprintf("golangci-lint --fix failed: %s", output)}
		}
	}

	return result
}

// runGoLint runs golangci-lint on files changed relative to the base branch
func (g *GitOperations) runGoLint(ctx context.Context, baseBranch string) (string, error) {
	output, err := g.runLintProgram(ctx, goLintArgs(g.resolveLintBase(ctx, baseBranch))...)
	outputStr := strings.TrimSpace(string(output))

	if err != nil {
		// Exit code 1 means issues found
		return outputStr, err
	}
	return outputStr, nil
}

// lintProgram is the lint program runGoLint and autoFixLint run.
const lintProgram = "golangci-lint"

// runLintProgram runs the lint program with args in the project directory and
// returns its combined output. With run_as_user empty it is the command of
// before. With the option set (TASK-651) the directory is the workspace of
// the agent user: the program runs as that user (agentCommand) with the
// tool environment of the git route (gitAgentBaseEnv, the allowlist,
// agentToolEnv: no ANTHROPIC_ or CLAUDE_ name), so the config of the repository
// is read and the files of the fix are written by the agent user. A missing
// program is read from the result of that run (lintProgramMissing) and
// returns what the lookup in PATH returns with the option empty: no output
// and exec.ErrNotFound. A command that is not a sudo command of the agent
// user (agentSudoCommandError) is a lintCommandRefusedError and runs nothing,
// never a lint that runs as the daemon user.
func (g *GitOperations) runLintProgram(ctx context.Context, args ...string) ([]byte, error) {
	s := runAsSettingsNow()
	if s == nil {
		cmd := exec.CommandContext(ctx, lintProgram, args...)
		cmd.Dir = g.projectPath
		return cmd.CombinedOutput()
	}
	cmd, err := agentCommand(ctx, lintProgram, args...)
	if err != nil {
		return nil, &lintCommandRefusedError{err: err}
	}
	if err := agentSudoCommandError(cmd, s, "lint"); err != nil {
		return nil, &lintCommandRefusedError{err: err}
	}
	cmd.Dir = g.projectPath
	setAgentToolEnvIn(cmd, gitAgentBaseEnv(), g.projectPath)
	output, err := cmd.CombinedOutput()
	if lintProgramMissing(output, err) {
		return nil, &exec.Error{Name: lintProgram, Err: exec.ErrNotFound}
	}
	return output, err
}

// lintCommandRefusedError is a lint command that runLintProgram did not run with
// run_as_user set: no command of the agent user could be built, or the one
// built is not a sudo command of the agent user (agentSudoCommandError).
type lintCommandRefusedError struct{ err error }

func (e *lintCommandRefusedError) Error() string { return e.err.Error() }
func (e *lintCommandRefusedError) Unwrap() error { return e.err }

// refusedLintCommand reports whether err is a lintCommandRefusedError.
func refusedLintCommand(err error) bool {
	var refused *lintCommandRefusedError
	return errors.As(err, &refused)
}

// lintProgramMissing reports whether a lint run as the agent user found no
// lint program: the "command not found" line with which sudo exits 1, or exit
// status 127 (the shell or exec of the command) with no output or with only
// the line of sudo or of the shell that says so. A present program that exits
// 127 with other output, a "no such file or directory" line of its own among
// them (GH-6668), is a lint failure with that output.
func lintProgramMissing(output []byte, err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	out := strings.TrimSpace(string(output))
	switch exitErr.ExitCode() {
	case 127:
		return out == "" || lintNotFoundLine(out)
	case 1:
		return out == "sudo: "+lintProgram+": command not found"
	}
	return false
}

// lintNotFoundLine reports whether out is the one line of sudo or of a shell
// that says the lint program is not found: "sudo: golangci-lint: command not
// found", "sh: 1: golangci-lint: not found", "bash: golangci-lint: command not
// found" and the like.
func lintNotFoundLine(out string) bool {
	if strings.Contains(out, "\n") || !strings.Contains(out, lintProgram) {
		return false
	}
	lower := strings.ToLower(out)
	return strings.HasSuffix(lower, "command not found") ||
		strings.HasSuffix(lower, ": not found")
}

// hookSettingsRelPath is the settings file into which the daemon writes its
// hook entries for a session, relative to the workspace.
const hookSettingsRelPath = ".claude/settings.json"

// stageAndAmendCommit stages all changes and amends the last commit. While the
// settings file still holds the entries of the daemon (hookSettingsLeftChanged)
// the file is taken out of the index again after the stage
// (unstageHookSettings, GH-6676).
func (g *GitOperations) stageAndAmendCommit(ctx context.Context, message string) error {
	// Stage all changes
	stageCmd := g.gitCmd(ctx, "add", "-A")
	if _, err := stageCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to stage changes: %w", err)
	}
	if err := g.unstageHookSettings(ctx); err != nil {
		return err
	}

	// Amend commit
	amendCmd := g.gitCmd(ctx, "commit", "--amend", "--no-edit")
	if _, err := amendCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to amend commit: %w", err)
	}

	return nil
}

// unstageHookSettings takes the settings file (hookSettingsRelPath) out of the
// index again while it still holds the entries of the daemon
// (hookSettingsLeftChanged), with one Debug line: the index then holds the
// file of HEAD, or no file. A path that git does not know (.claude/ ignored,
// .claude a symlink or a submodule) is not an error of the reset.
//
// Decision: a reset after the stage, not a pathspec exclude in the stage
// (GH-6676): git add exits 1 with an exclude when .claude/ is ignored and 128
// when .claude is a symlink or a submodule, and the commit then lacks the fix.
func (g *GitOperations) unstageHookSettings(ctx context.Context) error {
	if g.hookSettingsLeftChanged == nil || !g.hookSettingsLeftChanged() {
		return nil
	}
	resetCmd := g.gitCmd(ctx, "--literal-pathspecs", "reset", "-q", "--", hookSettingsRelPath)
	if output, err := resetCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to take %s out of the index: %w: %s", hookSettingsRelPath, err, output)
	}
	slog.Debug("the Claude settings file holds the hook entries of the daemon; the commit keeps it out",
		slog.String("path", filepath.Join(g.projectPath, filepath.FromSlash(hookSettingsRelPath))))
	return nil
}

// hasGoLintConfig checks if the project has golangci-lint config
func hasGoLintConfig(projectPath string) bool {
	// Check for common golangci-lint config file names
	configFiles := []string{
		".golangci.yml",
		".golangci.yaml",
		".golangci.toml",
		"golangci.yml",
		"golangci.yaml",
		"golangci.toml",
	}

	for _, cf := range configFiles {
		if lintFileExists(filepath.Join(projectPath, cf)) {
			return true
		}
	}

	// Also check for go.mod which indicates a Go project
	return lintFileExists(filepath.Join(projectPath, "go.mod"))
}

// lintFileExists checks if a file exists
func lintFileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
