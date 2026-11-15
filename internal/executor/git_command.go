package executor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// TASK-651 (design: .agent/system/agent-isolation-design.md, 4.2): this file
// is the one constructor of every git command of the package (the source test
// TestGitSourceNoInlineGitCommand keeps it that way). The directory a command
// runs in decides its route:
//
//   - plain route: run_as_user empty, or a directory outside the workspace
//     root. The command is exactly the inline exec.CommandContext(ctx, "git",
//     ...) of the site: same argv, same cmd.Dir, same cmd.Env.
//   - agent route: run_as_user set and a directory inside the workspace root.
//     The command comes from agentCommand (sudo, as the agent user) and its
//     environment is the allowlist plus the git variables of gitAgentEnv, so
//     no hook, filter or config value of a workspace runs as the daemon user.
//
// Every failure on the agent route sets cmd.Err: Run, Output, CombinedOutput
// and Start return it and no process starts. There is no fallback to the
// plain command.

// defaultRunAsWorkspaceDir is the workspace root when run_as_user is set and
// run_as_workspace_dir is empty.
const defaultRunAsWorkspaceDir = "/var/lib/pilot/agent-workspaces"

// gitAgentEnv is the fixed list of git variables a site may set on a git
// command (appendGitEnv). On the agent route they are the only names, besides
// the allowlist of setAgentEnv, that reach git; the credential names of
// withGitCredentials (GIT_ASKPASS, PILOT_GIT_TOKEN, GIT_TERMINAL_PROMPT) are
// not on it.
var gitAgentEnv = map[string]bool{
	// gitHardened and gitCommonDir (secret gate): no replace refs.
	"GIT_NO_REPLACE_OBJECTS": true,
	// rewriteCommitMessages: commit-tree keeps author and committer.
	"GIT_AUTHOR_NAME":     true,
	"GIT_AUTHOR_EMAIL":    true,
	"GIT_AUTHOR_DATE":     true,
	"GIT_COMMITTER_NAME":  true,
	"GIT_COMMITTER_EMAIL": true,
	"GIT_COMMITTER_DATE":  true,
}

// errGitCredentialsAgentRoute is the Err of a git command of the agent route
// after withGitCredentials: the agent user gets no GitHub token yet.
var errGitCredentialsAgentRoute = errors.New("run_as_user: no git credentials for a git command that runs as the agent user until the hand-over leg of TASK-651 merges")

// gitCmd builds a git command that runs in the project directory of g
// (cmd.Dir), routed by that directory.
func (g *GitOperations) gitCmd(ctx context.Context, args ...string) *exec.Cmd {
	return gitCommandIn(ctx, g.projectPath, args...)
}

// gitCommandIn builds a git command that runs in dir (cmd.Dir), routed by dir.
func gitCommandIn(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := gitCommand(ctx, dir, args...)
	cmd.Dir = dir
	return cmd
}

// gitCommand builds a git command routed by routeDir and leaves cmd.Dir unset:
// the form of the sites that pass `-C <dir>` in args, which give that dir.
func gitCommand(ctx context.Context, routeDir string, args ...string) *exec.Cmd {
	s := runAsSettingsNow()
	if s == nil {
		return exec.CommandContext(ctx, "git", args...)
	}
	agent, err := gitRoutesToAgent(s.workspaceRoot, routeDir)
	if err != nil {
		return failedGitCommand(args, err)
	}
	if !agent {
		return exec.CommandContext(ctx, "git", args...)
	}
	cmd, err := agentCommand(ctx, "git", args...)
	if err != nil {
		return failedGitCommand(args, fmt.Errorf("run_as_user: git in %s: %w", routeDir, err))
	}
	setAgentEnv(cmd, gitAgentBaseEnv())
	return cmd
}

// failedGitCommand is a command that never starts: every run returns err.
func failedGitCommand(args []string, err error) *exec.Cmd {
	return &exec.Cmd{Path: "git", Args: append([]string{"git"}, args...), Err: &gitRouteError{err: err}}
}

// gitRouteError is the Err of a command the constructor failed closed.
type gitRouteError struct{ err error }

func (e *gitRouteError) Error() string { return e.err.Error() }
func (e *gitRouteError) Unwrap() error { return e.err }

// gitRouteFailed reports whether cmd is a git command the constructor failed
// closed (failedGitCommand).
func gitRouteFailed(cmd *exec.Cmd) bool {
	var re *gitRouteError
	return errors.As(cmd.Err, &re)
}

// gitAgentBaseEnv is the environment of the daemon without the names of the
// model-env deny list, the input of setAgentEnv on the agent route.
//
// Decision: the allowlist keeps CLAUDE_* and ANTHROPIC_* for the model; a git
// process needs no model credential, and a hook of the workspace would read it.
func gitAgentBaseEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); !isModelEnvDenied(name) {
			env = append(env, kv)
		}
	}
	return env
}

// appendGitEnv is how a site sets git variables on a git command. Plain
// route: cmd.Env becomes os.Environ() (when unset) plus kv, as the inline
// sites did. Agent route: only the names of gitAgentEnv are added to the
// allowlisted environment, one entry per name, the last one wins, and the
// --preserve-env flag follows. A command the constructor failed is left alone.
func appendGitEnv(cmd *exec.Cmd, kv ...string) {
	if gitRouteFailed(cmd) {
		return
	}
	if _, agent := agentCmdUser(cmd); !agent {
		if cmd.Env == nil {
			cmd.Env = os.Environ()
		}
		cmd.Env = append(cmd.Env, kv...)
		return
	}
	env := cmd.Env
	for _, e := range kv {
		if name, _, ok := strings.Cut(e, "="); ok && gitAgentEnv[name] {
			env = append(env, e)
		}
	}
	env = lastEnvWins(env)
	cmd.Env = env
	setPreserveEnvNames(cmd, env)
}

// lastEnvWins keeps one entry per name, at the place of its first entry, with
// the value of its last.
func lastEnvWins(env []string) []string {
	last := map[string]string{}
	var order []string
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if _, seen := last[name]; !seen {
			order = append(order, name)
		}
		last[name] = kv
	}
	out := make([]string, 0, len(order))
	for _, name := range order {
		out = append(out, last[name])
	}
	return out
}

// gitRoutesToAgent reports whether a git command in dir takes the agent route:
// dir is root or below it, both compared after filepath.EvalSymlinks and
// filepath.Clean, so a root behind a symbolic link, a trailing slash or a
// /private prefix still matches. A dir that cannot be resolved is an error. A
// root that does not exist holds no workspace: plain route.
func gitRoutesToAgent(root, dir string) (bool, error) {
	realDir, err := resolveGitRoutePath(dir)
	if err != nil {
		return false, fmt.Errorf("run_as_user: resolve git directory %q: %w", dir, err)
	}
	if root == "" {
		return false, nil
	}
	realRoot, err := resolveGitRoutePath(root)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("run_as_user: resolve run_as_workspace_dir %q: %w", root, err)
	}
	rel, err := filepath.Rel(realRoot, realDir)
	if err != nil {
		return false, nil
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)), nil
}

// resolveGitRoutePath is the absolute, symlink-free, clean form of p; "" is
// the working directory of the daemon, where such a command runs.
func resolveGitRoutePath(p string) (string, error) {
	if p == "" {
		p = "."
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return filepath.Clean(real), nil
}
