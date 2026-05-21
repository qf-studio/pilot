// Package executor — TASK-286 / GH-3027
//
// Guardrail that refuses to create GitHub issues on repositories that are not
// in the user's configured project list. Closes the incident on 2026-05-20
// where an external Pilot user accidentally pointed his daemon at the upstream
// `qf-studio/pilot` repo and the epic decomposer fired 6 duplicate sub-issues
// (#3021-#3026). The decomposer at internal/executor/epic.go shelled out
// `gh issue create` with `cmd.Dir` set to the worktree path; `gh` infers
// owner/repo from the directory's origin remote, with no cross-check that
// the repo is one Pilot is supposed to write to.
//
// This file defines the chokepoint. Callers must hold a RepoAllowlist
// (concrete implementation lives in cmd/pilot/ to keep the executor
// decoupled from the top-level config types).
//
// Bypass: PILOT_ALLOW_UNMANAGED_REPO=1 (logs WARN with the resolved repo).

package executor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
)

// envBypassRepoAllowlist is the documented opt-out for the guardrail. When set
// to "1" the guardrail logs a WARN with the resolved owner/repo and returns
// nil, allowing the call to proceed. Intended for ad-hoc CLI invocations
// against repos that are not registered as Pilot projects; never the default.
const envBypassRepoAllowlist = "PILOT_ALLOW_UNMANAGED_REPO"

// ErrRepoNotInConfig is the sentinel returned when the resolved (owner, repo)
// pair does not match any project in the user's configured allowlist.
var ErrRepoNotInConfig = errors.New("target repo is not in user's configured project list")

// ErrNoOriginRemote is the sentinel returned when resolveGitRemote cannot
// determine an origin remote for the given working directory, or when the
// URL cannot be parsed into owner/repo.
var ErrNoOriginRemote = errors.New("no origin remote found")

// RepoAllowlist is the minimum surface ValidateTargetRepo needs to decide
// whether a (owner, repo, projectPath) tuple corresponds to a Pilot-managed
// project. Implementations live outside the executor package (typically
// wrapping *config.Config) so the executor stays decoupled from concrete
// configuration types and so the guardrail is trivial to unit-test.
type RepoAllowlist interface {
	// RepoIsAllowed reports whether (owner, repo) is configured for some
	// project. When projectPath is non-empty the match must also align with
	// that project's filesystem Path so a misconfigured executionPath cannot
	// inherit another project's git context.
	RepoIsAllowed(owner, repo, projectPath string) bool

	// ConfiguredRepos returns the set of "owner/repo" strings the user has
	// configured. Used only for error and log messages — must not include
	// secrets.
	ConfiguredRepos() []string
}

// ValidateTargetRepo returns nil iff (owner, repo) is allowed by the
// supplied allowlist for projectPath. On rejection it returns a wrapped
// ErrRepoNotInConfig. The PILOT_ALLOW_UNMANAGED_REPO=1 env var is honored as
// a documented bypass; the bypass path always logs a WARN naming the
// resolved repo so the action is visible in dashboards and audit logs.
//
// allow == nil is treated as "no allowlist plumbed" — the function refuses
// unless the bypass env var is set. This makes the safe default loud rather
// than silent if a code path forgets to set the allowlist on the Runner.
func ValidateTargetRepo(allow RepoAllowlist, owner, repo, projectPath string) error {
	if owner == "" || repo == "" {
		return fmt.Errorf("%w: empty owner or repo", ErrRepoNotInConfig)
	}

	if allow != nil && allow.RepoIsAllowed(owner, repo, projectPath) {
		return nil
	}

	if os.Getenv(envBypassRepoAllowlist) == "1" {
		configured := ""
		if allow != nil {
			configured = strings.Join(allow.ConfiguredRepos(), ",")
		}
		slog.Warn("PILOT_ALLOW_UNMANAGED_REPO=1 bypassed repo allowlist",
			"component", "executor.repo_guardrail",
			"owner", owner,
			"repo", repo,
			"project_path", projectPath,
			"configured_repos", configured,
		)
		return nil
	}

	if allow == nil {
		return fmt.Errorf("%w: no allowlist configured (set %s=1 to bypass for ad-hoc use)",
			ErrRepoNotInConfig, envBypassRepoAllowlist)
	}

	return fmt.Errorf("%w: %s/%s not in configured projects [%s]",
		ErrRepoNotInConfig, owner, repo, strings.Join(allow.ConfiguredRepos(), ","))
}

// resolveGitRemote returns the (owner, repo) parsed from the `origin` remote
// of the git working tree at dir. Supports the three URL forms produced by
// `git remote get-url`:
//
//	HTTPS:    https://github.com/owner/repo[.git]
//	SSH:      git@github.com:owner/repo[.git]
//	ssh://:   ssh://git@github.com/owner/repo[.git]
//
// Returns ErrNoOriginRemote if the directory has no remote or the URL
// cannot be parsed. ctx is used to bound the underlying `git` invocation.
func resolveGitRemote(ctx context.Context, dir string) (string, string, error) {
	if dir == "" {
		return "", "", fmt.Errorf("%w: empty directory", ErrNoOriginRemote)
	}
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("%w: git remote get-url origin: %v", ErrNoOriginRemote, err)
	}
	return parseGitHubRemoteURL(strings.TrimSpace(string(out)))
}

// parseGitHubRemoteURL parses one of the standard GitHub remote URL forms
// and returns (owner, repo). The .git suffix is stripped. Trailing slashes
// are tolerated. Returns ErrNoOriginRemote when the URL does not parse.
//
// The parser is intentionally conservative — it does NOT attempt to
// normalize hosts (e.g. github.enterprise.example.com); the only invariant
// is that the URL ends in "<owner>/<repo>" (with optional .git). For
// non-GitHub remotes the returned (owner, repo) is still meaningful and the
// allowlist check above will reject it if the user has not configured that
// remote as a project.
func parseGitHubRemoteURL(url string) (string, string, error) {
	if url == "" {
		return "", "", fmt.Errorf("%w: empty url", ErrNoOriginRemote)
	}

	s := strings.TrimSuffix(url, ".git")
	s = strings.TrimRight(s, "/")

	// SSH "scp-like" form: git@host:owner/repo
	// Distinguishable from URL forms by the presence of `:` without `//`.
	if !strings.Contains(s, "://") && strings.Contains(s, "@") && strings.Contains(s, ":") {
		colon := strings.Index(s, ":")
		if colon >= 0 && colon < len(s)-1 {
			return splitOwnerRepo(s[colon+1:], url)
		}
	}

	// URL forms: https://host/owner/repo or ssh://user@host/owner/repo
	if i := strings.Index(s, "://"); i >= 0 {
		rest := s[i+3:]
		// Drop optional user@ prefix.
		if at := strings.Index(rest, "@"); at >= 0 {
			rest = rest[at+1:]
		}
		// Drop host (up to first slash).
		if slash := strings.Index(rest, "/"); slash >= 0 {
			return splitOwnerRepo(rest[slash+1:], url)
		}
	}

	// Bare "owner/repo" fallback (uncommon, but tolerable).
	return splitOwnerRepo(s, url)
}

// splitOwnerRepo enforces that path == "owner/repo" with non-empty parts.
// The original URL is included in error messages to aid debugging.
func splitOwnerRepo(path, originalURL string) (string, string, error) {
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return "", "", fmt.Errorf("%w: expected owner/repo in %q", ErrNoOriginRemote, originalURL)
	}
	owner := parts[len(parts)-2]
	repo := parts[len(parts)-1]
	if owner == "" || repo == "" {
		return "", "", fmt.Errorf("%w: empty owner or repo in %q", ErrNoOriginRemote, originalURL)
	}
	return owner, repo, nil
}
