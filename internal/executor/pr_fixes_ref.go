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

// fixesRefRe matches an explicit GitHub closing-keyword reference ("Fixes
// #N", "Closes #N", "Resolves #N", case-insensitive) anywhere in an issue
// body.
var fixesRefRe = regexp.MustCompile(`(?i)\b(?:fixes|closes|resolves)\s+#(\d+)\b`)

// extraFixesKeyword scans description for fixesRefRe matches and returns a
// "\nFixes #N" line for each referenced issue number distinct from
// ownIssueNum, deduplicated in first-seen order. Callers append the result
// alongside the task's own "Closes #<ownIssueNum>" line when building a PR
// body, so a sub-issue that explicitly names a further issue to close (e.g.
// the external report a decomposed epic ultimately resolves) carries that
// reference forward as a real closing keyword on the generated PR — not
// merely as quoted text inside the copied description.
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
