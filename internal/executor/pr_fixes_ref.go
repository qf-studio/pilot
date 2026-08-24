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
// live auto-close keywords on the generated PR. GH-5198 tightened this
// further: GH-5191's line-anchored shape used a single-character terminator
// class (any of `.,;:!?)`) that only checked the character immediately
// after the number, so "Closes #123, but only partially — needs follow-up"
// still matched even though real prose followed the comma; and the
// unconditional quoted shape promoted a quoted marker regardless of
// surrounding context, so "Do not write 'closes #123' in the PR body" (a
// negated instruction) still matched. A structured marker is now one of
// exactly three shapes:
//   - line-anchored: at the start of a line (optionally after a
//     "-"/"*"/"•" list bullet, e.g. a "## Refs" section entry), terminated
//     by end-of-line or a single bare trailing period — NOT by a comma,
//     semicolon, colon, or other punctuation that could introduce trailing
//     prose ("Closes #123, but only partially" no longer matches), OR
//   - line-anchored and quoted: a quoted marker that itself starts the
//     line (optionally after a bullet), with nothing else inside the
//     quotes, OR
//   - cue-prefixed and quoted: a quoted marker immediately preceded by an
//     imperative cue ("include"/"includes"/"including"/"add"/"adds"/
//     "adding") — the GH-5165 repro, "Ensure the PR body includes 'Fixes
//     #5149'.". A quoted marker with no line-anchor and no imperative cue
//     ("Do not write 'closes #123' in the PR body") matches none of the
//     three shapes and is intentionally left as plain text, since the cue
//     requirement is what distinguishes "insert this marker" instructions
//     from "don't write/never say this marker" negations.
//
// Free-standing narrative usage — the keyword appearing after a subject
// ("this bug closes #123...") — matches none of the three shapes and is
// intentionally left as plain text.
var fixesRefRe = regexp.MustCompile(`(?im)(?:^[ \t]*(?:[-*•]\s+)?(?:fixes|closes|resolves)\s+#(\d+)(?:\.|[ \t]*$)|^[ \t]*(?:[-*•]\s+)?['"“‘](?:fixes|closes|resolves)\s+#(\d+)['"”’]|(?:includes?|including|adds?|adding)\s+['"“‘](?:fixes|closes|resolves)\s+#(\d+)['"”’])`)

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
		// fixesRefRe has three alternative shapes, each with its own
		// capture group (line-anchored, line-anchored+quoted,
		// cue-prefixed+quoted) — exactly one is non-empty per match.
		n := m[1]
		if n == "" {
			n = m[2]
		}
		if n == "" {
			n = m[3]
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		b.WriteString("\nFixes #")
		b.WriteString(n)
	}
	return b.String()
}
