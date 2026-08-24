// Package executor — GH-5165 (sibling of GH-5153's approval-authorization
// epic): a decomposed sub-issue's body sometimes names a closing-keyword
// reference to a different issue than the sub-issue's own number — e.g. a
// wrap-up chore whose body reads "Ensure the PR body includes 'Fixes
// #5149'" where #5149 is the original external report the whole epic
// ultimately resolves. Pilot's generated PR body already embeds the task's
// full description verbatim, so that reference is present as *text*, but
// nothing makes it a real GitHub closing keyword of the generated PR itself
// (as opposed to happening to be quoted inside the copied description).
// This file makes that propagation explicit and deliberate rather than
// incidental.
package executor

import (
	"regexp"
	"strings"
)

// fixesRefRe matches a *structured* GitHub closing-keyword marker ("Fixes
// #N", "Closes #N", "Resolves #N", case-insensitive) — GH-5191: the
// original version matched the keyword+number pair anywhere in prose, which
// promoted incidental, quoted, negated, or descriptive mentions ("this bug
// closes #123 prematurely", "the old fix resolves #99 only partially") into
// live auto-close keywords on the generated PR. A structured marker is one
// that stands alone as its own unit rather than being embedded mid-sentence:
//   - at the start of a line (optionally after a "-"/"*"/"•" list bullet,
//     e.g. a "## Refs" section entry), terminated by end-of-line, sentence
//     punctuation, or a closing bracket, OR
//   - wrapped in quotes with nothing else inside them (the GH-5165 repro:
//     body text reading "Ensure the PR body includes 'Fixes #5149'.").
//
// Free-standing narrative usage — the keyword appearing after a subject
// ("this bug closes #123...") — matches neither shape and is intentionally
// left as plain text.
var fixesRefRe = regexp.MustCompile(`(?im)(?:^[ \t]*(?:[-*•]\s+)?|['"“‘])(?:fixes|closes|resolves)\s+#(\d+)(?:['"”’]|[.,;:!?)]|[ \t]*$)`)

// extraFixesKeyword scans description for fixesRefRe matches and returns a
// "\nFixes #N" line for each referenced issue number distinct from
// ownIssueNum, deduplicated in first-seen order. Callers append the result
// alongside the task's own "Closes #<ownIssueNum>" line when building a PR
// body, so a sub-issue that explicitly names a further issue to close (e.g.
// the external report a decomposed epic ultimately resolves) carries that
// reference forward as a real closing keyword on the generated PR — not
// merely as quoted text inside the copied description.
//
// GH-5191: the emitted "Fixes #N" line is GitHub closing-keyword syntax.
// Callers on non-GitHub PR/MR creation paths (GitLab MRs and, per the
// PRCreator interface's own doc comment, any future Azure DevOps/Jira/
// Linear implementation) must NOT call this — the referenced #N could be a
// same-project GitLab IID, a GitHub issue number quoted from an epic's
// original report, or something else entirely, and there is no reliable way
// to tell which without adapter-specific context. Those paths should either
// skip promotion or build their own adapter-native equivalent.
func extraFixesKeyword(description, ownIssueNum string) string {
	matches := fixesRefRe.FindAllStringSubmatch(description, -1)
	if len(matches) == 0 {
		return ""
	}

	seen := map[string]bool{ownIssueNum: true}
	var b strings.Builder
	for _, m := range matches {
		n := m[1]
		if seen[n] {
			continue
		}
		seen[n] = true
		b.WriteString("\nFixes #")
		b.WriteString(n)
	}
	return b.String()
}
