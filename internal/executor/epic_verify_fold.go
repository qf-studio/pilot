package executor

import (
	"regexp"
	"strings"
)

// verifyShapePositiveRe / verifyShapeNegativeRe mirror the verification-shape
// heuristic from the sibling dependency detector (dependency_detector.go,
// GH-4234/TASK-402): positive phrases mark a subtask as checking prior work
// rather than building something new; negative phrases signal genuine new
// implementation and override a positive match. Duplicated here rather than
// referenced directly because GH-4234 had not landed on main at the time this
// subtask (GH-4235) executed — dedupe into one shared definition once both
// are merged.
var verifyShapePositiveRe = regexp.MustCompile(`(?i)\b(verify|confirm|run the acceptance|regression[\s-]test)\w*\b`)
var verifyShapeNegativeRe = regexp.MustCompile(`(?i)\b(add|fix|implement)\w*\b`)

// backtickSpanPattern extracts inline-code spans (“ `like this` “) from
// planning prose, used by implementationSurface to pick out symbol names that
// aren't file paths.
var backtickSpanPattern = regexp.MustCompile("`([^`]+)`")

// implementationSurface extracts the set of files and symbols a planned
// subtask's title+description stakes out as its own work. Used by
// foldVerifyOnlySubtasks to tell a pure verification step (touches nothing
// new) from one that introduces its own implementation surface despite
// verification-shaped language.
func implementationSurface(text string) map[string]bool {
	surface := make(map[string]bool)

	for _, m := range filePathPattern.FindAllString(text, -1) {
		surface["file:"+m] = true
	}

	for _, m := range backtickSpanPattern.FindAllStringSubmatch(text, -1) {
		token := strings.TrimSpace(m[1])
		if token == "" || strings.ContainsAny(token, " \t\n") {
			continue
		}
		if filePathPattern.MatchString(token) {
			continue // already captured as a file target above
		}
		surface["symbol:"+token] = true
	}

	return surface
}

// isVerifyOnlySubtask reports whether st reads as pure verification of prev's
// work: it must match the verification-shape heuristic (and not the
// implementation-shape override), and every file/symbol it names must already
// belong to prev's implementation surface — i.e. it introduces nothing new.
func isVerifyOnlySubtask(st, prev PlannedSubtask) bool {
	text := st.Title + "\n" + st.Description
	if !verifyShapePositiveRe.MatchString(text) || verifyShapeNegativeRe.MatchString(text) {
		return false
	}

	surface := implementationSurface(text)
	if len(surface) == 0 {
		return true
	}

	prevSurface := implementationSurface(prev.Title + "\n" + prev.Description)
	for target := range surface {
		if !prevSurface[target] {
			return false
		}
	}
	return true
}

// foldAcceptanceCriteria appends a verify-only subtask's acceptance criteria
// into the predecessor's description under a shared section. Checkbox-style
// criteria lines (reusing decompose.go's extractAcceptanceCriteria) are
// preferred; when the verify-only description has none, its full text is
// folded in as a single criterion.
func foldAcceptanceCriteria(prevDesc, verifyDesc string) string {
	criteria := extractAcceptanceCriteria(verifyDesc)
	if len(criteria) == 0 {
		trimmed := strings.TrimSpace(verifyDesc)
		if trimmed == "" {
			return prevDesc
		}
		criteria = []string{trimmed}
	}

	var sb strings.Builder
	sb.WriteString(strings.TrimRight(prevDesc, "\n"))
	sb.WriteString("\n\n## Acceptance Criteria (folded)\n")
	for _, c := range criteria {
		sb.WriteString("- [ ] ")
		sb.WriteString(strings.TrimSpace(c))
		sb.WriteString("\n")
	}
	return sb.String()
}

// foldVerifyOnlySubtasks collapses planned subtasks that are pure
// verification of the immediately preceding subtask into that predecessor,
// dropping the now-redundant verify-only entry (GH-4235/GH-4233). An epic
// planner sometimes emits a trailing subtask that only re-checks the prior
// subtask's deliverable ("verify X works", "confirm zero regressions")
// without adding any file or symbol of its own — creating a sub-issue for
// that step produces a child with nothing left to implement.
//
// Ordering is preserved and folding only ever targets the immediate
// surviving predecessor — a verify-only entry never reaches back past one
// that was itself folded away.
func foldVerifyOnlySubtasks(subtasks []PlannedSubtask) []PlannedSubtask {
	if len(subtasks) < 2 {
		return subtasks
	}

	folded := make([]PlannedSubtask, 0, len(subtasks))
	folded = append(folded, subtasks[0])

	for _, st := range subtasks[1:] {
		prev := &folded[len(folded)-1]
		if isVerifyOnlySubtask(st, *prev) {
			prev.Description = foldAcceptanceCriteria(prev.Description, st.Description)
			continue
		}
		folded = append(folded, st)
	}

	return folded
}
