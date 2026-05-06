package executor

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// conventionalCommitRegex matches titles in the form:
//
//	type(scope)?!?: description
//
// where type is one of the standard conventional commit types. Duplicated
// here intentionally — the autopilot package owns release-time parsing,
// and this copy gates PR creation at write time (GH-2325).
var conventionalCommitRegex = regexp.MustCompile(
	`^(feat|fix|docs|refactor|test|chore|ci|build|perf|revert|style)(\([^)]+\))?!?:\s+.+`,
)

// issuePrefixRegex strips adapter-specific issue prefixes that Pilot prepends
// to PR titles (e.g. "GH-2325: ", "APP-123: ") before conventional-commit
// validation. The downstream squash-merge path strips the same prefix
// (see internal/autopilot/auto_merger.go).
var issuePrefixRegex = regexp.MustCompile(`^[A-Z][A-Z0-9]*-\d+:\s+`)

// ErrNonConventionalTitle is returned when a title does not match the
// conventional commit format and could not be auto-corrected. Callers use
// errors.Is to distinguish this from transport failures.
var ErrNonConventionalTitle = errors.New("title is not a conventional commit")

// labelPrefixMap maps issue labels (lowercased) to conventional commit types.
// Keep narrow — only labels that map unambiguously to a commit type.
var labelPrefixMap = map[string]string{
	"bug":           "fix",
	"bugfix":        "fix",
	"enhancement":   "feat",
	"feature":       "feat",
	"docs":          "docs",
	"documentation": "docs",
	"refactor":      "refactor",
	"refactoring":   "refactor",
	"test":          "test",
	"tests":         "test",
	"chore":         "chore",
	"ci":            "ci",
	"build":         "build",
	"perf":          "perf",
	"performance":   "perf",
}

// validatePRTitle returns nil when title matches conventional commit format,
// accepting an optional issue-id prefix like "GH-123: " that Pilot prepends.
func validatePRTitle(title string) error {
	trimmed := strings.TrimSpace(title)
	if trimmed == "" {
		return fmt.Errorf("%w: empty title", ErrNonConventionalTitle)
	}
	stripped := issuePrefixRegex.ReplaceAllString(trimmed, "")
	if !conventionalCommitRegex.MatchString(stripped) {
		return fmt.Errorf("%w: %q", ErrNonConventionalTitle, truncateTitle(trimmed, 80))
	}
	return nil
}

// autoPrefixTitle prepends a conventional commit prefix derived from issue
// labels. Returns the new title and true on success; if no label maps, the
// original title and false.
func autoPrefixTitle(title string, labels []string) (string, bool) {
	trimmed := strings.TrimSpace(title)
	if trimmed == "" {
		return trimmed, false
	}
	for _, label := range labels {
		key := strings.ToLower(strings.TrimSpace(label))
		if prefix, ok := labelPrefixMap[key]; ok {
			return prefix + ": " + trimmed, true
		}
	}
	return trimmed, false
}

// inferConventionalPrefix derives a conventional commit type from file-level
// diff stats and issue labels. First-match-wins across the priority order
// defined in the acceptance criteria (GH-2735).
func inferConventionalPrefix(diff GitDiff, labels []string) string {
	files := diff.Files

	if len(files) > 0 {
		if allFilesMatch(files, func(f string) bool {
			ext := strings.ToLower(filepath.Ext(f))
			base := strings.ToLower(filepath.Base(f))
			return ext == ".md" || ext == ".mdx" || base == "readme" || base == "readme.md" || base == "readme.mdx"
		}) {
			return "docs"
		}

		if allFilesMatch(files, func(f string) bool {
			base := filepath.Base(f)
			return strings.HasSuffix(base, "_test.go") ||
				strings.HasSuffix(base, "_test.ts") ||
				strings.Contains(base, ".test.")
		}) {
			return "test"
		}
	}

	for _, label := range labels {
		key := strings.ToLower(strings.TrimSpace(label))
		if prefix, ok := labelPrefixMap[key]; ok {
			return prefix
		}
	}

	if len(files) > 0 {
		if allFilesMatch(files, func(f string) bool {
			return strings.HasPrefix(f, ".github/workflows/") ||
				strings.HasPrefix(f, ".gitlab-ci") ||
				f == ".gitlab-ci.yml"
		}) {
			return "ci"
		}

		buildFiles := map[string]bool{
			"dockerfile": true, "makefile": true, "go.mod": true, "go.sum": true,
			"package.json": true, "package-lock.json": true,
		}
		if allFilesMatch(files, func(f string) bool {
			return buildFiles[strings.ToLower(filepath.Base(f))]
		}) {
			return "build"
		}

		total := diff.Added + diff.Removed
		if total > 0 && hasCodeFile(files) {
			if diff.Added >= 2*diff.Removed {
				return "feat"
			}
			ratio := float64(abs(diff.Added-diff.Removed)) / float64(total)
			if ratio < 0.2 {
				return "refactor"
			}
		}
	}

	return "chore"
}

func allFilesMatch(files []string, pred func(string) bool) bool {
	if len(files) == 0 {
		return false
	}
	for _, f := range files {
		if !pred(f) {
			return false
		}
	}
	return true
}

func hasCodeFile(files []string) bool {
	nonCodeExts := map[string]bool{".md": true, ".mdx": true, ".txt": true, ".json": true, ".yaml": true, ".yml": true}
	for _, f := range files {
		ext := strings.ToLower(filepath.Ext(f))
		if !nonCodeExts[ext] {
			return true
		}
	}
	return false
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// normalizeTitle returns a conventional-commit title derived from title, labels,
// and diff stats. If title already conforms it is returned as-is. Otherwise
// auto-prefix is attempted from labels, then from the diff heuristic.
// Only errors on empty title.
func normalizeTitle(title string, labels []string, diff GitDiff) (string, error) {
	trimmed := strings.TrimSpace(title)
	if trimmed == "" {
		return "", fmt.Errorf("%w: empty title", ErrNonConventionalTitle)
	}

	// Strip leading issue-id prefix (e.g. "GH-2735: ") before trying to normalize.
	subject := issuePrefixRegex.ReplaceAllString(trimmed, "")
	// Lowercase first word of subject for consistency.
	subject = lowercaseFirst(subject)
	// Truncate to 72 chars.
	subject = truncateTitle(subject, 72)

	// 1. Already a valid conventional commit — return as-is (preserving original casing/prefix).
	if err := validatePRTitle(trimmed); err == nil {
		return trimmed, nil
	}

	// 2. Label-derived prefix.
	if prefixed, ok := autoPrefixTitle(subject, labels); ok {
		if err := validatePRTitle(prefixed); err == nil {
			return prefixed, nil
		}
	}

	// 3. Diff-derived prefix (last-ditch fallback, GH-2735).
	prefix := inferConventionalPrefix(diff, labels)
	candidate := prefix + ": " + subject
	if err := validatePRTitle(candidate); err == nil {
		return candidate, nil
	}

	return trimmed, fmt.Errorf("%w: could not auto-correct %q", ErrNonConventionalTitle, truncateTitle(trimmed, 80))
}

// lowercaseFirst lowercases the first rune of s, leaving the rest unchanged.
func lowercaseFirst(s string) string {
	if s == "" {
		return s
	}
	runes := []rune(s)
	runes[0] = unicode.ToLower(runes[0])
	return string(runes)
}
