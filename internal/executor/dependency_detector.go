package executor

import (
	"regexp"
	"strconv"
)

// DependencyReason identifies why a sub-issue is treated as depending on a
// prior sibling's merged state, for the selective merge-wait gate in
// executeSubIssuesTracked (GH-4234 / TASK-402).
type DependencyReason string

const (
	// DependencyNone is the default: no dependency detected, so the child may
	// start without waiting for any prior sibling's PR to merge. This keeps
	// wait_for_merge:false the effective global default for independent
	// siblings — see TASK-402.
	DependencyNone DependencyReason = "none"

	// DependencyExplicitRef means the child's title/description names a prior
	// sibling via "Depends on: #N" or "Blocked by: #N".
	DependencyExplicitRef DependencyReason = "explicit_ref"

	// DependencyVerificationShape means the child's title/description reads as
	// verifying/confirming earlier work (e.g. "verify…", "confirm zero
	// hits…", "run the acceptance…", "regression-test…") rather than
	// implementing something new — treated as depending on the immediately
	// preceding sibling.
	DependencyVerificationShape DependencyReason = "verification_shape"
)

// dependencyRefRe matches an explicit "Depends on: #123" / "Blocked by: #123"
// reference anywhere in a sub-issue's title or description text.
var dependencyRefRe = regexp.MustCompile(`(?i)\b(?:depends on|blocked by)\s*:?\s*#(\d+)`)

// verificationPositiveRe matches phrases that mark a sub-issue as verifying or
// confirming prior work: "verify…", "confirm zero hits…", "run the
// acceptance…", "regression-test…".
var verificationPositiveRe = regexp.MustCompile(`(?i)\b(verify|confirm|run the acceptance|regression[\s-]test)\w*\b`)

// verificationNegativeRe matches phrases that mark genuine new implementation
// work. These override a verification-shape match — a child that both
// verifies and implements is still new work, not a pure verification child.
var verificationNegativeRe = regexp.MustCompile(`(?i)\b(add|fix|implement)\w*\b`)

// detectChildDependency inspects a sub-issue's title+description text and
// decides whether it depends on a prior sibling in the current epic's child
// set, and so must wait for that sibling's PR to merge (+ a main-branch sync)
// before it starts.
//
// siblingNumbers scopes the explicit-ref check (GH-4234): an explicit
// "Depends on: #N" is only honored when N belongs to the current epic's own
// child set — a reference to the epic parent or an unrelated issue elsewhere
// in the tracker must not force a wait.
func detectChildDependency(title, description string, siblingNumbers map[int]bool) (bool, DependencyReason) {
	text := title + "\n" + description

	for _, m := range dependencyRefRe.FindAllStringSubmatch(text, -1) {
		num, err := strconv.Atoi(m[1])
		if err == nil && siblingNumbers[num] {
			return true, DependencyExplicitRef
		}
	}

	if verificationPositiveRe.MatchString(text) && !verificationNegativeRe.MatchString(text) {
		return true, DependencyVerificationShape
	}

	return false, DependencyNone
}

// siblingIssueNumbers builds the set of GitHub issue numbers for every child
// in the current epic decomposition, used to scope detectChildDependency's
// explicit-ref check to siblings only.
func siblingIssueNumbers(issues []CreatedIssue) map[int]bool {
	set := make(map[int]bool, len(issues))
	for _, issue := range issues {
		if issue.Number > 0 {
			set[issue.Number] = true
		}
	}
	return set
}

// subIssueDisplayRef returns the human-readable reference for a sub-issue
// used in dependency log lines: the numeric GitHub issue number when present,
// otherwise the adapter Identifier (e.g. Linear/Jira key).
func subIssueDisplayRef(issue CreatedIssue) string {
	if issue.Number > 0 {
		return strconv.Itoa(issue.Number)
	}
	if issue.Identifier != "" {
		return issue.Identifier
	}
	return "unknown"
}
