package github

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
)

// conventionalCommitRE matches conventional commit titles per the spec used by Pilot.
var conventionalCommitRE = regexp.MustCompile(`^(feat|fix|chore|refactor|test|docs|perf|build|ci|style)(\([^)]+\))?: .+$`)

// envBypassIssueAllowlist is the env var that bypasses the repo allowlist check at
// the CreatePilotIssue level. Must match executor.envBypassRepoAllowlist.
const envBypassIssueAllowlist = "PILOT_ALLOW_UNMANAGED_REPO"

// IssueAllowlist is the minimal surface CreatePilotIssue needs to validate that the
// target (owner, repo) is in the user's configured project list. executor.RepoAllowlist
// satisfies this interface — callers can pass it directly. When nil, the check is skipped
// but a WARN is logged so the omission is visible in audit logs.
//
// This interface is defined here (rather than importing executor.RepoAllowlist) to avoid
// an import cycle: internal/config imports internal/adapters/github, so neither executor
// nor config can be imported from this package.
type IssueAllowlist interface {
	// RepoIsAllowed reports whether (owner, repo) is a configured Pilot project.
	RepoIsAllowed(owner, repo, projectPath string) bool
	// ConfiguredRepos returns "owner/repo" strings for error messages only.
	ConfiguredRepos() []string
}

// allowAllIssueRepos is an IssueAllowlist that permits any (owner, repo). Use it
// at call sites where owner/repo are already constrained by explicit config (e.g.
// the autopilot feedback loop) so the intent is encoded at the call site rather
// than relying on a permissive nil default. TASK-347.
type allowAllIssueRepos struct{}

func (allowAllIssueRepos) RepoIsAllowed(string, string, string) bool { return true }
func (allowAllIssueRepos) ConfiguredRepos() []string                 { return []string{"*"} }

// AllowAllIssueRepos returns an IssueAllowlist that permits any repo, for callers
// whose target is already constrained by their own configuration. TASK-347.
func AllowAllIssueRepos() IssueAllowlist { return allowAllIssueRepos{} }

// CreatePilotIssue validates the title against the conventional-commits format and
// creates a GitHub issue with the given labels.
//
// allow is the repo allowlist for TASK-286 / GH-3027 defense-in-depth. Pass a non-nil
// IssueAllowlist to enforce that (owner, repo) is a configured Pilot project. A nil
// allowlist now FAILS CLOSED (TASK-347) to match executor.ValidateTargetRepo — for a
// caller whose owner/repo is already constrained by its own config, pass
// AllowAllIssueRepos() to encode that intent. Set PILOT_ALLOW_UNMANAGED_REPO=1 to bypass
// (covers both the nil case and a non-nil allowlist that would otherwise reject the repo).
//
// Returns an error if the title does not match conventional-commits format, if the
// allowlist rejects the repo, or if the GitHub API call fails.
func CreatePilotIssue(ctx context.Context, c *Client, allow IssueAllowlist, owner, repo, title, body string, labels []string) (*Issue, error) {
	if err := validateIssueRepo(allow, owner, repo); err != nil {
		return nil, fmt.Errorf("CreatePilotIssue repo guardrail: %w", err)
	}
	if !conventionalCommitRE.MatchString(title) {
		return nil, fmt.Errorf("issue title %q does not match conventional-commits format (type(scope): description)", title)
	}
	return c.CreateIssue(ctx, owner, repo, &IssueInput{
		Title:  title,
		Body:   body,
		Labels: labels,
	})
}

// validateIssueRepo enforces the repo allowlist. Mirrors executor.ValidateTargetRepo
// but is defined here to avoid the executor→github import cycle.
func validateIssueRepo(allow IssueAllowlist, owner, repo string) error {
	bypass := os.Getenv(envBypassIssueAllowlist) == "1"

	if allow == nil {
		// C7 (TASK-347): fail closed to match executor.ValidateTargetRepo — a future
		// caller that forgets to wire an allowlist must not silently get zero
		// enforcement (the GH-3027 cross-repo-leak class). Known-safe callers pass
		// AllowAllIssueRepos() to encode "intentionally unrestricted" explicitly.
		if bypass {
			slog.Warn("CreatePilotIssue: no IssueAllowlist configured; PILOT_ALLOW_UNMANAGED_REPO=1 bypassed the repo check",
				"component", "adapters.github.issue_create",
				"owner", owner,
				"repo", repo,
			)
			return nil
		}
		return fmt.Errorf("no IssueAllowlist configured for %s/%s — pass an allowlist (or AllowAllIssueRepos()) or set %s=1",
			owner, repo, envBypassIssueAllowlist)
	}

	if allow.RepoIsAllowed(owner, repo, "") {
		return nil
	}

	if bypass {
		slog.Warn("PILOT_ALLOW_UNMANAGED_REPO=1 bypassed IssueAllowlist",
			"component", "adapters.github.issue_create",
			"owner", owner,
			"repo", repo,
			"configured_repos", strings.Join(allow.ConfiguredRepos(), ","),
		)
		return nil
	}

	return fmt.Errorf("%s/%s not in configured projects [%s]",
		owner, repo, strings.Join(allow.ConfiguredRepos(), ","))
}
