package autopilot

import (
	"fmt"
	"regexp"

	"github.com/qf-studio/pilot/internal/adapters/github"
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

// extractTypeScope returns the conventional-commit type and scope from a title.
// Returns ("", "") if the title doesn't match the conventional-commit prefix.
// Example: "feat(auth): add OAuth" -> ("feat", "auth").
func extractTypeScope(title string) (string, string) {
	m := convCommitRE.FindStringSubmatch(title)
	if m == nil {
		return "", ""
	}
	return m[1], m[2]
}

// ScopeDriftReason returns a non-empty reason if the PR's conventional-commit
// type or scope diverges from the linked issue's. Empty string = no drift
// (or insufficient signal — both titles must have a conventional prefix).
//
// Closes the cascade-2 attack surface where a `fix(upgrade)` issue produced a
// `feat(auth)` PR. We only escalate (force human approval), never block.
func ScopeDriftReason(prTitle, issueTitle string) string {
	if prTitle == "" || issueTitle == "" {
		return ""
	}
	prType, prScope := extractTypeScope(prTitle)
	issueType, issueScope := extractTypeScope(issueTitle)
	// If either side has no conventional prefix, we can't compare — abstain.
	if prType == "" || issueType == "" {
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

// SizeFloorThreshold is the net-additions threshold above which a PR escalates
// to human approval regardless of env config. Cascade #2's bad PR was 512 LoC;
// 200 catches that with comfortable margin and only blocks PRs that genuinely
// merit a second pair of eyes.
const SizeFloorThreshold = 200

// SizeFloorReason returns a non-empty reason if the PR's net additions exceed
// SizeFloorThreshold. Pilot's median PR is well under 100 additions — anything
// over 200 is large enough that auto-merge is too aggressive even on a passing
// CI signal.
func SizeFloorReason(files []*github.PRFile) string {
	var net int
	for _, f := range files {
		net += f.Additions
	}
	if net > SizeFloorThreshold {
		return fmt.Sprintf("PR adds %d net lines (> %d threshold)", net, SizeFloorThreshold)
	}
	return ""
}
