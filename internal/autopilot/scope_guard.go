package autopilot

import (
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// Born from OAuth cascade #2 (2026-05-04). Two structural rails to ensure that
// a runaway executor cannot land code unsupervised even if env config drops
// require_approval. Both gates only ESCALATE — they force human approval, they
// never silently merge. They never relax an already-required approval.
//
// #2584 ScopeDriftReason: PR title type/scope must match the linked issue's.
//   - Issue 'fix(upgrade): X' producing PR 'feat(auth): Y'  → blocked.
//   - Same gate would have blocked PR #2572 (cascade #2 entry point).
// #2585 SizeFloorReason: any PR > 200 net added lines escalates regardless of
// env config. Cascade #2's contaminating PR was 512 LoC.

var convCommitRE = regexp.MustCompile(`^([a-z]+)\(([a-z0-9_./-]+)\)\s*[!:]`)

// issueRefPrefixRE matches a leading issue-reference tag such as "GH-3785: ",
// "JIRA-123: ", or "TASK-12: " that Pilot workers prepend to PR titles.
// GH-3827: these prefixes shifted the conventional-commit type off string
// start, so convCommitRE silently failed to match and the scope-drift gate
// abstained on every prefixed worker PR (observed on #3796, #3816).
var issueRefPrefixRE = regexp.MustCompile(`(?i)^[a-z]+-\d+:\s*`)

// stripIssueRefPrefix removes leading issue-reference tags (e.g. "GH-3785: ")
// so the conventional-commit type/scope can be matched at the new start.
// Strips repeatedly in case more than one tag is stacked.
func stripIssueRefPrefix(title string) string {
	for {
		stripped := issueRefPrefixRE.ReplaceAllString(title, "")
		if stripped == title {
			return title
		}
		title = stripped
	}
}

// extractTypeScope returns the conventional-commit type and scope from a title.
// Returns ("", "") if the title doesn't match the conventional-commit prefix,
// after stripping any leading issue-reference tag (e.g. "GH-3785: ").
// Example: "feat(auth): add OAuth" -> ("feat", "auth").
// Example: "GH-3785: fix(executor): X" -> ("fix", "executor").
func extractTypeScope(title string) (string, string) {
	m := convCommitRE.FindStringSubmatch(stripIssueRefPrefix(title))
	if m == nil {
		return "", ""
	}
	return m[1], m[2]
}

// ScopeDriftReason returns a non-empty reason if the PR's conventional-commit
// type or scope diverges from the linked issue's. Empty string = no drift
// (or insufficient signal — both titles must have a conventional prefix).
// logger may be nil (e.g. in tests); when non-nil, abstentions are logged at
// INFO so a silently-bypassed gate is visible (GH-3827 — a genuinely
// unparseable title used to abstain without a trace).
//
// Closes the cascade-2 attack surface where a `fix(upgrade)` issue produced a
// `feat(auth)` PR. We only escalate (force human approval), never block.
func ScopeDriftReason(logger *slog.Logger, prTitle, issueTitle string) string {
	if prTitle == "" || issueTitle == "" {
		if logger != nil {
			logger.Info("scope-drift gate abstained: empty PR or issue title",
				"pr_title", prTitle, "issue_title", issueTitle)
		}
		return ""
	}
	prType, prScope := extractTypeScope(prTitle)
	issueType, issueScope := extractTypeScope(issueTitle)
	// If either side has no conventional prefix, we can't compare — abstain.
	if prType == "" || issueType == "" {
		if logger != nil {
			logger.Info("scope-drift gate abstained: no conventional-commit shape found",
				"pr_title", prTitle, "issue_title", issueTitle,
				"pr_parsed", prType != "", "issue_parsed", issueType != "")
		}
		return ""
	}
	if prType != issueType {
		return fmt.Sprintf("PR title type %q diverges from issue title type %q", prType, issueType)
	}
	if prScope != "" && issueScope != "" && prScope != issueScope {
		return fmt.Sprintf("PR title scope %q diverges from issue title scope %q", prScope, issueScope)
	}
	return ""
}

// pilotBranchPrefix is the branch-name convention Pilot worker branches use
// for issue-scoped PRs (e.g. "pilot/GH-4599").
const pilotBranchPrefix = "pilot/GH-"

// issueNumberFromBranch extracts the issue number from a "pilot/GH-N" branch
// name, using the same fmt.Sscanf convention as resolveIssueNumFromPR
// (controller.go). Returns 0, false if branchName doesn't carry the prefix or
// no digits follow it — e.g. a scope-release carrier's empty BranchName, or a
// human-authored branch.
func issueNumberFromBranch(branchName string) (int, bool) {
	if !strings.HasPrefix(branchName, pilotBranchPrefix) {
		return 0, false
	}
	var n int
	if _, err := fmt.Sscanf(branchName, pilotBranchPrefix+"%d", &n); err != nil || n == 0 {
		return 0, false
	}
	return n, true
}

// scopeDriftIssueNumber resolves which issue number handleCIPassed should
// fetch for ScopeDriftReason's comparison against the PR title. GH-4599: the
// caller used to pass prState.IssueNumber straight through, but for a
// scope-release carrier PR (see epicParentFromScopeKey in scope_release.go)
// that value is the EPIC PARENT's issue number, not the PR's own originating
// issue — walking to the epic parent compared the carrier's title against
// the epic's title, the wrong signal for scope drift. Deriving the number
// from the "pilot/GH-N" branch-name convention instead targets the PR's own
// issue. Falls back to fallbackIssueNumber (the walk-to-epic-parent value)
// with an INFO log line when branchName doesn't match the convention —
// carrier PRs have no BranchName set, and human-authored PRs use arbitrary
// branch names, so the fallback preserves prior behavior there. The reason
// string ScopeDriftReason returns is unaffected: only the issue we compare
// against changes, not the format of the drift message.
func scopeDriftIssueNumber(logger *slog.Logger, branchName string, fallbackIssueNumber int) int {
	if n, ok := issueNumberFromBranch(branchName); ok {
		return n
	}
	if logger != nil {
		logger.Info("scope-drift issue resolution: branch has no pilot/GH-N pattern, falling back to linked issue number",
			"branch", branchName, "fallback_issue", fallbackIssueNumber)
	}
	return fallbackIssueNumber
}

// SizeFloorThreshold is the net-additions threshold above which a PR escalates
// to human approval regardless of env config. Cascade #2's bad PR was 512 LoC.
// GH-3570: raised from 200 to 500 — routine well-scoped Pilot PRs with tests
// (e.g. #3559 at +656) tripped the old floor; 500 still catches the cascade-2
// class while sparing ordinary multi-file fixes.
const SizeFloorThreshold = 500

// isBookkeepingPath reports whether path is Navigator bookkeeping/generated
// content (`.agent/**`, e.g. task docs, the knowledge graph) rather than
// shipped code. GH-4055: these files inflate PR additions with no bearing on
// the risk the size-floor gate exists to catch — a large `.agent/` diff
// (task docs, graph.json regen) should never itself trigger escalation.
func isBookkeepingPath(path string) bool {
	return path == ".agent" || strings.HasPrefix(path, ".agent/")
}

// isTestPath reports whether path is a Go test file. Test additions inflate
// PR size without adding production surface area — the same rationale
// isBookkeepingPath already applies to `.agent/**`.
func isTestPath(path string) bool {
	return strings.HasSuffix(path, "_test.go")
}

// productionAdditions splits a PR's file list into production additions
// (shipped code) versus bookkeeping (isBookkeepingPath, e.g. `.agent/**`) and
// test (isTestPath, `_test.go`) additions. Both the CI-fix size guard
// (controller.go) and SizeFloorReason use this so they can never drift apart
// on what counts as "real" size again — GH-4284: the CI-fix guard summed raw
// additions with no exclusions at all, auto-closing a well-tested PR (#4279)
// whose 421 additions were ~290 test + 28 bookkeeping and only ~90 production.
func productionAdditions(files []*github.PRFile) (production, bookkeeping, test int) {
	for _, f := range files {
		switch {
		case isBookkeepingPath(f.Filename):
			bookkeeping += f.Additions
		case isTestPath(f.Filename):
			test += f.Additions
		default:
			production += f.Additions
		}
	}
	return production, bookkeeping, test
}

// excludedAdditionsSuffix renders the "excluded" parenthetical shared by
// SizeFloorReason and the CI-fix size guard, e.g. "; 28 bookkeeping
// additions excluded, 290 test additions excluded". Returns "" if nothing
// was excluded, so callers can fall back to a plain "no exclusions" message.
func excludedAdditionsSuffix(bookkeeping, test int) string {
	switch {
	case bookkeeping > 0 && test > 0:
		return fmt.Sprintf("; %d bookkeeping additions excluded, %d test additions excluded", bookkeeping, test)
	case bookkeeping > 0:
		return fmt.Sprintf("; %d bookkeeping additions excluded", bookkeeping)
	case test > 0:
		return fmt.Sprintf("; %d test additions excluded", test)
	default:
		return ""
	}
}

// SizeFloorReason returns a non-empty reason if the PR's production
// additions (excluding Navigator bookkeeping paths and Go test files, see
// productionAdditions) exceed SizeFloorThreshold. Pilot's median PR is well
// under 100 additions — anything over the floor is large enough that
// auto-merge is too aggressive even on a passing CI signal. GH-4055: PR
// #4047 (586 total additions, 281 of them `.agent/**`, 305 code) wrongly
// escalated under the old net-additions count — bookkeeping additions are
// tracked separately and excluded from the gate, while still surfaced in the
// reason string for visibility. GH-4284: test additions are now excluded
// the same way.
func SizeFloorReason(files []*github.PRFile) string {
	production, bookkeeping, test := productionAdditions(files)
	if production > SizeFloorThreshold {
		if suffix := excludedAdditionsSuffix(bookkeeping, test); suffix != "" {
			return fmt.Sprintf("PR adds %d code additions (> %d threshold%s)", production, SizeFloorThreshold, suffix)
		}
		return fmt.Sprintf("PR adds %d net lines (> %d threshold)", production, SizeFloorThreshold)
	}
	return ""
}
