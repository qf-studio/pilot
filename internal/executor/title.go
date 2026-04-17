package executor

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
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

// normalizeTitle returns a conventional-commit title derived from title and
// labels. If title already conforms it is returned as-is. Otherwise auto-prefix
// is attempted from labels. If neither succeeds the result wraps
// ErrNonConventionalTitle so callers can abort PR creation.
func normalizeTitle(title string, labels []string) (string, error) {
	trimmed := strings.TrimSpace(title)
	if err := validatePRTitle(trimmed); err == nil {
		return trimmed, nil
	}
	if prefixed, ok := autoPrefixTitle(trimmed, labels); ok {
		if err := validatePRTitle(prefixed); err == nil {
			return prefixed, nil
		}
	}
	return trimmed, fmt.Errorf("%w: could not auto-correct %q", ErrNonConventionalTitle, truncateTitle(trimmed, 80))
}
