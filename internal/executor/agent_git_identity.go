package executor

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
)

// TASK-651: the git identity of the agent user. A workspace has no user.name
// and no user.email, the agent user has no global git config, so a commit as
// the agent user (the commit of the model, the commit that keeps unfinished
// work, the amend of the co-author strip) would fail with "Please tell me who
// you are". With the option set every command of the agent user gets the
// identity of the daemon's repository in the environment (setAgentEnv).

// Defaults of agentGitIdentityEnv for a value the daemon's repository does not set.
const (
	defaultAgentGitName  = "Pilot"
	defaultAgentGitEmail = "pilot@localhost"
)

// agentGitIdentities holds the identity of each daemon's repository by PROJECT,
// the part of a workspace name derived from that repository
// (workspaceProjectID). createAgentWorkspace stores it before the first
// command of the agent user in the workspace.
var agentGitIdentities sync.Map // string -> []string

// agentGitIdentityEnv returns GIT_AUTHOR_NAME, GIT_AUTHOR_EMAIL,
// GIT_COMMITTER_NAME and GIT_COMMITTER_EMAIL with user.name and user.email of
// daemonRepo, read by git as the daemon user; a value that is empty or cannot
// be read is defaultAgentGitName or defaultAgentGitEmail.
func agentGitIdentityEnv(ctx context.Context, daemonRepo string) []string {
	name := daemonGitConfigValue(ctx, daemonRepo, "user.name")
	if name == "" {
		name = defaultAgentGitName
	}
	email := daemonGitConfigValue(ctx, daemonRepo, "user.email")
	if email == "" {
		email = defaultAgentGitEmail
	}
	return gitIdentityEnv(name, email)
}

// gitIdentityEnv is the author and committer variables of git for name and email.
func gitIdentityEnv(name, email string) []string {
	return []string{
		"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email,
		"GIT_COMMITTER_NAME=" + name, "GIT_COMMITTER_EMAIL=" + email,
	}
}

// daemonGitConfigValue is `git config --get key` in repo as the daemon user,
// "" when unset or when the command would run as the agent user.
func daemonGitConfigValue(ctx context.Context, repo, key string) string {
	if repo == "" {
		return ""
	}
	cmd := gitCommandIn(ctx, repo, "config", "--get", key)
	if _, agent := agentCmdUser(cmd); agent {
		return ""
	}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// rememberAgentGitIdentity stores the identity of daemonRepo for the
// workspaces of project.
func rememberAgentGitIdentity(ctx context.Context, project, daemonRepo string) {
	agentGitIdentities.Store(project, agentGitIdentityEnv(ctx, daemonRepo))
}

// agentGitIdentityFor is the identity of a command of the agent user in dir:
// the stored identity of the daemon's repository of the workspace dir is in,
// the defaults for any other directory (a model process outside a workspace,
// a probe).
func agentGitIdentityFor(s *runAsSettings, dir string) []string {
	if project, ok := agentWorkspaceProjectOf(s, dir); ok {
		if v, found := agentGitIdentities.Load(project); found {
			return v.([]string)
		}
	}
	return gitIdentityEnv(defaultAgentGitName, defaultAgentGitEmail)
}

// agentWorkspaceProjectOf returns PROJECT of the workspace dir is in: the name
// of the direct child of the workspace root (as given or resolved) that holds
// dir.
func agentWorkspaceProjectOf(s *runAsSettings, dir string) (string, bool) {
	if s == nil || s.workspaceRoot == "" || dir == "" {
		return "", false
	}
	d, err := absCleanPath(dir)
	if err != nil {
		return "", false
	}
	roots := []string{s.workspaceRoot}
	if r, err := resolveGitRoutePath(s.workspaceRoot); err == nil {
		roots = append(roots, r)
	}
	for _, root := range roots {
		rel, err := filepath.Rel(filepath.Clean(root), d)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		first, _, _ := strings.Cut(rel, string(filepath.Separator))
		if project, _, ok := parseAgentWorkspaceName(first); ok {
			return project, true
		}
	}
	return "", false
}
