package ghguard

import (
	"fmt"
	"strings"
)

// prCreateHeadFlags are the flag spellings for `gh pr create`'s --head.
var prCreateHeadFlags = []string{"-H", "--head"}

// apiMethodFlags are the flag spellings for `gh api`'s HTTP method override.
var apiMethodFlags = []string{"-X", "--method"}

// issueCommentValueFlags are `gh issue comment` flags that consume the next
// token as a value, so firstPositional doesn't mistake a flag's value for
// the issue target.
var issueCommentValueFlags = []string{"-b", "--body", "-F", "--body-file", "-R", "--repo", "--edit-last"}

// prCommentValueFlags are `gh pr comment` flags that consume the next token
// as a value.
var prCommentValueFlags = []string{"-b", "--body", "-F", "--body-file", "-R", "--repo", "--edit-last"}

// checkAPI allows `gh api` only for GET requests (the default method when
// -X/--method is omitted). Does not depend on TaskContext — an unauthenticated
// read of any endpoint carries the same risk profile as gh's other
// unconditional read allowances, so it isn't gated on task identity.
func checkAPI(rest []string, _ TaskContext) Verdict {
	if method, ok := extractFlagValue(rest, apiMethodFlags); ok {
		if !strings.EqualFold(method, "GET") {
			return deny(fmt.Sprintf("gh api method %s is not GET", strings.ToUpper(method)))
		}
	}
	return allow("gh api GET")
}

// checkPRCreate allows `gh pr create` only when --head is omitted (implicit:
// gh infers the currently checked-out branch, which is always the task's own
// worktree branch) or explicitly names the task's own branch.
func checkPRCreate(rest []string, ctx TaskContext) Verdict {
	if ctx.Branch == "" {
		return deny("task branch unknown; cannot verify pr create ownership")
	}
	if head, ok := extractFlagValue(rest, prCreateHeadFlags); ok {
		if head != ctx.Branch {
			return deny(fmt.Sprintf("--head %q does not match task branch %q", head, ctx.Branch))
		}
	}
	return allow("pr create for task branch")
}

// checkIssueComment allows `gh issue comment` only when the explicit issue
// target (gh always requires one — there is no implicit "current issue")
// resolves to the task's own dispatched issue.
func checkIssueComment(rest []string, ctx TaskContext) Verdict {
	if ctx.Issue == "" {
		return deny("task issue unknown; cannot verify issue comment ownership")
	}
	target, ok := firstPositional(rest, issueCommentValueFlags)
	if !ok {
		return deny("gh issue comment requires an explicit issue number/url; none given")
	}
	num, parsed := normalizeIssueRef(target)
	if !parsed || num != ctx.Issue {
		return deny(fmt.Sprintf("targets issue %q, task is scoped to issue #%s", target, ctx.Issue))
	}
	return allow("own-issue comment")
}

// checkPRComment allows `gh pr comment` when the target is omitted (implicit:
// gh infers the PR for the currently checked-out branch) or explicitly names
// the task's own branch. An explicit PR number or URL can't be verified
// locally against the task branch without another gh call, so it's denied —
// mirrors GH-4671's decision to fail closed rather than trust an
// unverifiable target.
func checkPRComment(rest []string, ctx TaskContext) Verdict {
	if ctx.Branch == "" {
		return deny("task branch unknown; cannot verify pr comment ownership")
	}
	target, ok := firstPositional(rest, prCommentValueFlags)
	if !ok {
		return allow("implicit pr comment on current branch")
	}
	if target == ctx.Branch {
		return allow("own-branch pr comment")
	}
	return deny(fmt.Sprintf("targets %q; only the implicit current branch or explicit %q is accepted", target, ctx.Branch))
}

// extractFlagValue scans argv for the first occurrence of any flag spelling
// in names and returns its value, supporting "--flag value", "--flag=value",
// "-f value", and "-fvalue" (short-flag-concatenated-value) forms.
func extractFlagValue(argv []string, names []string) (string, bool) {
	for i, a := range argv {
		for _, name := range names {
			if a == name {
				if i+1 < len(argv) {
					return argv[i+1], true
				}
				return "", false
			}
			if strings.HasPrefix(name, "--") && strings.HasPrefix(a, name+"=") {
				return strings.TrimPrefix(a, name+"="), true
			}
			if !strings.HasPrefix(name, "--") && len(name) == 2 && strings.HasPrefix(a, name) && len(a) > len(name) {
				return strings.TrimPrefix(a, name), true
			}
		}
	}
	return "", false
}

// firstPositional returns the first non-flag token in argv, skipping any
// recognized value-flag (and the token that supplies its value) so a flag
// value like `--body "close it"` isn't mistaken for the positional target.
// Unrecognized flags are assumed boolean (no value token consumed) — safe
// for the narrow set of subcommands this is used against, since every gh
// flag not in valueFlagNames for issue/pr comment is in fact boolean.
func firstPositional(argv []string, valueFlagNames []string) (string, bool) {
	isValueFlag := func(tok string) bool {
		for _, n := range valueFlagNames {
			if tok == n {
				return true
			}
		}
		return false
	}
	for i := 0; i < len(argv); i++ {
		tok := argv[i]
		if strings.HasPrefix(tok, "-") {
			if strings.Contains(tok, "=") {
				continue
			}
			if isValueFlag(tok) {
				i++ // also skip the value token
			}
			continue
		}
		return tok, true
	}
	return "", false
}

// normalizeIssueRef extracts a bare issue number from a positional gh
// argument that may be "1234", "#1234", or a full issue/PR URL.
func normalizeIssueRef(s string) (string, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "#")
	if strings.Contains(s, "github.com/") {
		if idx := strings.LastIndex(s, "/"); idx >= 0 {
			s = s[idx+1:]
		}
	}
	if s == "" {
		return "", false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	return s, true
}

// normalizeRepo lowercases and strips a github.com URL prefix / trailing
// slash or .git suffix, so "https://github.com/Owner/Repo.git" and
// "owner/repo" compare equal.
func normalizeRepo(s string) string {
	s = strings.TrimSpace(s)
	if idx := strings.Index(s, "github.com/"); idx >= 0 {
		s = s[idx+len("github.com/"):]
	}
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")
	return strings.ToLower(s)
}

// repoMatches reports whether target (a -R/--repo flag value) refers to the
// same repo as taskRepo. An empty taskRepo (task identity not wired) never
// matches — the caller can't verify, so it fails closed.
func repoMatches(target, taskRepo string) bool {
	if taskRepo == "" {
		return false
	}
	return normalizeRepo(target) == normalizeRepo(taskRepo)
}
