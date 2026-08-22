package executor

import (
	"regexp"
	"strconv"
	"strings"
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

// ExtractDependencyRefs returns every issue/PR number referenced via an
// explicit "Depends on: #N" / "Blocked by: #N" marker in body, in
// first-seen order with duplicates removed (GH-5045/GH-5052).
//
// This generalizes detectChildDependency's dependencyRefRe usage by
// dropping the siblingNumbers scoping: that scoping exists because
// detectChildDependency runs at decomposition time, when the current
// epic's own child set is known and a ref to an unrelated issue elsewhere
// in the tracker must not force a wait. The dispatch-time base-presence
// check this feeds (base_presence.go) has no such sibling context — it
// runs once per queued issue, independent of any epic — so every explicit
// ref is a candidate prerequisite worth probing.
func ExtractDependencyRefs(body string) []int {
	if body == "" {
		return nil
	}
	seen := make(map[int]bool)
	var refs []int
	for _, m := range dependencyRefRe.FindAllStringSubmatch(body, -1) {
		num, err := strconv.Atoi(m[1])
		if err != nil || num <= 0 || seen[num] {
			continue
		}
		seen[num] = true
		refs = append(refs, num)
	}
	return refs
}

// backtickSpanRe matches the content of a single-line backtick-quoted span,
// e.g. the `internal/foo.go` in "see `internal/foo.go` for details".
var backtickSpanRe = regexp.MustCompile("`([^`\n]+)`")

// pathLineRefSuffixRe strips an optional trailing "line" or "line-range"
// reference (":42" or ":42-58") so a citation like
// `internal/executor/runner.go:42` still resolves to the bare file path.
var pathLineRefSuffixRe = regexp.MustCompile(`:\d+(?:-\d+)?$`)

// referencedPathIgnorePrefixes excludes URLs from ExtractReferencedPaths —
// a backtick-quoted link is not a repo file path even though it may
// contain slashes and a dot.
var referencedPathIgnorePrefixes = []string{"http://", "https://"}

// globMetacharacters is every character that turns a backtick-quoted span
// from a checkable file path into a *description of files* — a glob pattern
// (GH-5133): asterisk/question-mark wildcards, character classes (`[...]`),
// and brace sets (`{...}`). A glob is never itself a file on disk, so
// probing it via FileExistsOnDefaultBranch (base_presence.go) always 404s
// and always produces a false-positive hold — the GH-5133 incident's root
// cause, where a perfectly natural citation like `cmd/pilot/*.go` wedged the
// dispatch queue for 3 hours on a phantom "prerequisite not on main".
const globMetacharacters = "*?[]{}"

// ExtractReferencedPaths returns every backtick-quoted repo-relative file
// path mentioned in body, in first-seen order with duplicates removed
// (GH-5045/GH-5052).
//
// Heuristic, validated by hand against real issue bodies (GH-5021, ui
// GH-120/124/139): backtick content counts as a path when it (1) is not a
// URL, (2) contains a "/", (3) contains no whitespace, (4) contains no glob
// metacharacter (GH-5133: `*`, `?`, `[`, `]`, `{`, `}` — a glob names a set
// of files, not one checkable prerequisite), and (5) ends in a
// dot-extension once an optional trailing ":<line>" or ":<start>-<end>"
// line-ref suffix is stripped. This is intentionally conservative — it
// catches genuine file citations (`internal/boardapi/dto.go`,
// `internal/fleet/tenantres.go:42`) while excluding shell commands
// (`aws s3 cp`, `make test`), bare API routes with no extension
// (`/api/v1/orgs`), extensionless filenames with no path separator
// (`0008_board.up.sql`), and glob patterns (`cmd/pilot/*.go`,
// `internal/**/*_test.go`, `pkg/{a,b}/main.go`, `file[0-9].go`).
func ExtractReferencedPaths(body string) []string {
	if body == "" {
		return nil
	}
	seen := make(map[string]bool)
	var paths []string
	for _, m := range backtickSpanRe.FindAllStringSubmatch(body, -1) {
		candidate := strings.TrimSpace(m[1])
		if candidate == "" || strings.ContainsAny(candidate, " \t") {
			continue
		}

		isURL := false
		for _, prefix := range referencedPathIgnorePrefixes {
			if strings.HasPrefix(candidate, prefix) {
				isURL = true
				break
			}
		}
		if isURL || !strings.Contains(candidate, "/") {
			continue
		}

		if strings.ContainsAny(candidate, globMetacharacters) {
			continue
		}

		stripped := pathLineRefSuffixRe.ReplaceAllString(candidate, "")
		if !hasFileExtension(stripped) {
			continue
		}

		if seen[stripped] {
			continue
		}
		seen[stripped] = true
		paths = append(paths, stripped)
	}
	return paths
}

// hasFileExtension reports whether s ends in a "." followed by at least one
// character within its final path segment (i.e. the dot is not itself part
// of a directory component).
func hasFileExtension(s string) bool {
	idx := strings.LastIndex(s, ".")
	if idx < 0 || idx == len(s)-1 {
		return false
	}
	return !strings.Contains(s[idx:], "/")
}
