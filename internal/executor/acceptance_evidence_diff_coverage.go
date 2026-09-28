package executor

import (
	"fmt"
	"regexp"
	"strings"
)

// Diff-coverage check (GH-5466).
//
// The acceptance-evidence gate above classifies command-shaped acceptance
// items (paste-output, mutation) and runs them; everything else is left
// alone. It has no view of the diff at all, so an issue that names a
// specific file the PR must edit gets no check beyond the acceptance
// checklist's own wording. Two consecutive pilot-console PRs (PR#332 for
// GH-331, PR#334 for GH-333, 2026-09-28) each implemented one half of a
// two-file change and left the other, explicitly named file untouched; both
// PR bodies restated the issue's acceptance list, one silently dropping the
// unmet criterion. Both merged on green CI. The omission was visible from
// the diff alone — the issue named the file, the PR's diff didn't touch it.
//
// This file adds a diff-coverage check: collect file paths named in the
// issue body's own "what to change" sections (Change/Changes/
// Implementation/Fix, plus Acceptance, since acceptance criteria routinely
// restate a named file too), and compare against the PR branch's actual
// diff. A named path the diff never touches is reported — reporting only,
// no hard block, exactly like the rest of the Not-verified list this merges
// into (see runDiffCoverageCheck in acceptance_evidence_run.go for the
// git-backed wiring, and appendAcceptanceEvidence for where the two note
// classes are merged).

// diffCoverageSectionHeadingRe matches an H2 ("## Heading") or H3
// ("### Heading") markdown heading line, capturing the heading text.
// Matching is line-anchored ((?m)) so a "##" appearing mid-sentence (e.g.
// inside a fenced code block) is not mistaken for a heading — every
// observed issue body puts headings at the start of their own line.
var diffCoverageSectionHeadingRe = regexp.MustCompile(`(?m)^(#{2,3})[ \t]+(.+?)[ \t]*$`)

// diffCoverageIncludedSections is the case-insensitive allow-list of
// section headings the diff-coverage check reads paths from. Every other
// heading (Context, Problem, Refs, Out of scope, ...) is excluded by
// omission — a path cited there describes background or an explicit
// non-goal, not a required edit.
var diffCoverageIncludedSections = map[string]bool{
	"change":         true,
	"changes":        true,
	"implementation": true,
	"fix":            true,
	"acceptance":     true,
}

// issueSection is one heading-delimited section of an issue body: the
// heading text (verbatim, not lower-cased) and the content up to (not
// including) the next heading.
type issueSection struct {
	Heading string
	Body    string
}

// splitIssueSections splits body into sections on H2 ("## ") and H3
// ("### ") markdown headings, case-insensitively matched at the call site
// (isDiffCoverageSection) rather than here. Content before the first
// heading is dropped: a diff-coverage citation is only actionable once it's
// under a named section, and the issue templates this gate targets always
// open with a heading before any body content worth scanning.
func splitIssueSections(body string) []issueSection {
	locs := diffCoverageSectionHeadingRe.FindAllStringSubmatchIndex(body, -1)
	if len(locs) == 0 {
		return nil
	}

	sections := make([]issueSection, 0, len(locs))
	for i, loc := range locs {
		// loc[4]/loc[5] bound submatch group 2 (the heading text).
		heading := strings.TrimSpace(body[loc[4]:loc[5]])

		bodyStart := loc[1] // end of the full heading-line match
		bodyEnd := len(body)
		if i+1 < len(locs) {
			bodyEnd = locs[i+1][0]
		}

		sections = append(sections, issueSection{Heading: heading, Body: body[bodyStart:bodyEnd]})
	}
	return sections
}

// isDiffCoverageSection reports whether heading (verbatim issue text) is one
// of the sections the diff-coverage check reads paths from.
func isDiffCoverageSection(heading string) bool {
	return diffCoverageIncludedSections[strings.ToLower(strings.TrimSpace(heading))]
}

// ExtractDiffCoveragePaths returns every backtick-quoted file path (using
// ExtractReferencedPaths' resolver — dependency_detector.go) named under a
// Change/Changes/Implementation/Fix or Acceptance heading in body, in
// first-seen order with duplicates removed. Paths cited only under other
// headings (Context, Problem, Refs, Out of scope, ...) are excluded.
func ExtractDiffCoveragePaths(body string) []string {
	if body == "" {
		return nil
	}

	seen := make(map[string]bool)
	var paths []string
	for _, section := range splitIssueSections(body) {
		if !isDiffCoverageSection(section.Heading) {
			continue
		}
		for _, p := range ExtractReferencedPaths(section.Body) {
			if seen[p] {
				continue
			}
			seen[p] = true
			paths = append(paths, p)
		}
	}
	return paths
}

// DiffCoverageNotVerifiedReason is the fixed Not-verified message for a path
// named in the issue's Change/Acceptance sections that the PR's diff does
// not touch (GH-5466 spec wording, verbatim).
func DiffCoverageNotVerifiedReason(path string) string {
	return fmt.Sprintf("%s is named in the issue but the PR does not modify it", path)
}

// CheckDiffCoverage compares the paths ExtractDiffCoveragePaths finds in
// issueBody against changedFiles (the PR branch's `git diff --name-only`
// vs. base), and returns the subset of named paths that are (a) not present
// in changedFiles and (b) reported present on the base branch by
// existsOnBase. A path absent from base entirely is base-presence's class
// of finding (base_presence.go's FileExistsOnDefaultBranch gate), not this
// one's — skipping it here avoids reporting the same root cause twice under
// two different gates. existsOnBase == nil skips that filter (reports every
// uncovered path) — production callers always supply it (see
// runDiffCoverageCheck, acceptance_evidence_run.go); tests exercising the
// "not on base" exclusion supply a fake.
func CheckDiffCoverage(issueBody string, changedFiles []string, existsOnBase func(path string) bool) []string {
	changed := make(map[string]bool, len(changedFiles))
	for _, f := range changedFiles {
		changed[f] = true
	}

	var uncovered []string
	for _, path := range ExtractDiffCoveragePaths(issueBody) {
		if changed[path] {
			continue
		}
		if existsOnBase != nil && !existsOnBase(path) {
			continue
		}
		uncovered = append(uncovered, path)
	}
	return uncovered
}
