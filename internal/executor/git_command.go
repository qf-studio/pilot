package executor

import (
	"context"
	"errors"
	"fmt"
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
//     ...) of the site (through execCommandContext, which tests wrap): same
//     argv, same cmd.Dir, same cmd.Env. A `-C` or `--git-dir` into the root
//     from a directory outside it fails closed (gitArgIntoRoot).
//   - agent route: run_as_user set and a directory inside the workspace root.
//     The command comes from agentCommand (sudo, as the agent user) and its
//     environment is the allowlist plus the git variables of gitAgentEnv, so
//     no hook, filter or config value of a workspace runs as the daemon user.
//
// Every failure on the agent route sets cmd.Err: Run, Output, CombinedOutput
// and Start return it and no process starts. There is no fallback to the
// plain command.

// defaultRunAsWorkspaceDir is the workspace root when run_as_user is set and
// run_as_workspace_dir is empty: the workspaces directory below the agent root
// that scripts/box/setup-agent-user.sh creates in the data directory.
const defaultRunAsWorkspaceDir = "/var/lib/pilot/agent/workspaces"

// defaultRunAsDataDir is the data directory of the daemon when run_as_user is
// set and run_as_data_dir is empty: the default of the setup script.
const defaultRunAsDataDir = "/var/lib/pilot"

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
		return execCommandContext(ctx, "git", args...)
	}
	agent, err := gitRoutesToAgent(s.workspaceRoot, routeDir)
	if err != nil {
		return failedGitCommand(args, err)
	}
	if !agent {
		if target := gitArgIntoRoot(s.workspaceRoot, routeDir, args); target != "" {
			return failedGitCommand(args, fmt.Errorf("run_as_user: git argument %q points inside the workspace root %q, the command runs in %q", target, s.workspaceRoot, routeDir))
		}
		return execCommandContext(ctx, "git", args...)
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

// gitRoutesToAgent reports whether a git command in dir takes the agent route.
// A directory is inside the root when its cleaned absolute path as given is
// below the root (as given or resolved), or when its resolved path is below
// the resolved root; the root itself counts. The two answers must agree: a
// symbolic link inside a workspace that points at a repository of the daemon,
// or a path from outside that reaches a workspace through a link, is an error,
// never a command that runs as the daemon user. A dir that cannot be resolved
// is an error. A root that cannot be resolved fails only the directories
// inside it as given; every other directory takes the plain route.
func gitRoutesToAgent(root, dir string) (bool, error) {
	realDir, err := resolveGitRoutePath(dir)
	if err != nil {
		return false, fmt.Errorf("run_as_user: resolve git directory %q: %w", dir, err)
	}
	if root == "" {
		return false, nil
	}
	givenDir, err := absCleanPath(dir)
	if err != nil {
		return false, fmt.Errorf("run_as_user: git directory %q: %w", dir, err)
	}
	givenRoot, err := absCleanPath(root)
	if err != nil {
		return false, fmt.Errorf("run_as_user: run_as_workspace_dir %q: %w", root, err)
	}
	realRoot, err := resolveGitRoutePath(root)
	if err != nil {
		if pathInside(givenRoot, givenDir) || pathInside(givenRoot, realDir) {
			return false, fmt.Errorf("run_as_user: resolve run_as_workspace_dir %q: %w", root, err)
		}
		return false, nil
	}
	givenIn := pathInside(givenRoot, givenDir) || pathInside(realRoot, givenDir)
	realIn := pathInside(realRoot, realDir)
	if givenIn != realIn {
		return false, fmt.Errorf("run_as_user: git directory %q resolves to %q: inside the workspace root %q in only one of the two forms", dir, realDir, root)
	}
	return realIn, nil
}

// gitValueOptions are the global options of git that take their value as the
// next argument, besides -C, --git-dir and --work-tree: that argument is not
// the subcommand.
var gitValueOptions = map[string]bool{"-c": true, "--namespace": true, "--attr-source": true, "--config-env": true}

// gitArgIntoRoot returns the first `-C` or `--git-dir` (also `--work-tree`)
// value among the global options of args that points inside the workspace
// root, as given or resolved, "" when none does. Every argument before the
// subcommand is read, also after an option that takes a value
// (gitValueOptions). Relative values are taken from routeDir; `-C` values add
// up as git adds them.
func gitArgIntoRoot(root, routeDir string, args []string) string {
	if root == "" {
		return ""
	}
	base := routeDir
	if base == "" {
		base = "."
	}
	join := func(v string) string {
		if filepath.IsAbs(v) {
			return v
		}
		return filepath.Join(base, v)
	}
	inside := func(p string) bool {
		for _, r := range pathForms(root) {
			for _, f := range pathForms(p) {
				if pathInside(r, f) {
					return true
				}
			}
		}
		return false
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		var target string
		switch {
		case a == "-C" && i+1 < len(args):
			i++
			base = join(args[i])
			target = base
		case (a == "--git-dir" || a == "--work-tree") && i+1 < len(args):
			i++
			target = join(args[i])
		case strings.HasPrefix(a, "--git-dir="), strings.HasPrefix(a, "--work-tree="):
			_, v, _ := strings.Cut(a, "=")
			target = join(v)
		case gitValueOptions[a] && i+1 < len(args):
			i++
			continue
		case strings.HasPrefix(a, "-"):
			continue
		default:
			return "" // the subcommand: its own options are not paths of git
		}
		if inside(target) {
			return target
		}
	}
	return ""
}

// pathForms returns the absolute clean form of p and, when it differs and
// can be resolved, its resolved form. Nil when p has no absolute form.
func pathForms(p string) []string {
	given, err := absCleanPath(p)
	if err != nil {
		return nil
	}
	forms := []string{given}
	if real, err := resolveGitRoutePath(p); err == nil && real != given {
		forms = append(forms, real)
	}
	return forms
}

// pathInside reports whether p is root or below it; both must be clean and
// absolute.
func pathInside(root, p string) bool {
	if root == "" || p == "" {
		return false
	}
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// absCleanPath is the absolute, clean form of p without resolving links; ""
// is the working directory of the daemon.
func absCleanPath(p string) (string, error) {
	if p == "" {
		p = "."
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

// resolveGitRoutePath is the absolute, symlink-free, clean form of p; "" is
// the working directory of the daemon, where such a command runs.
func resolveGitRoutePath(p string) (string, error) {
	abs, err := absCleanPath(p)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return filepath.Clean(real), nil
}
