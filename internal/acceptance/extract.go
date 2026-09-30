// Package acceptance extracts acceptance-criteria items from issue bodies.
//
// It is a leaf package (no imports from internal/executor or
// internal/adapters) so both sides can share one extractor: the adapters
// import internal/executor, so the executor could not import an adapter's
// copy. GH-5491: the dispatcher rebuilds tasks from the persisted executions
// row and must re-extract criteria itself.
package acceptance

import (
	"regexp"
	"strings"
)

var (
	// CheckboxItemRe matches a markdown "- [ ] item" / "- [x] item" line and
	// captures the item text in group 1.
	CheckboxItemRe = regexp.MustCompile(`- \[[ x]\] (.+)`)
	// ListItemRe matches a plain markdown "- item" line and captures the item
	// text in group 1.
	ListItemRe = regexp.MustCompile(`- (.+)`)

	// houseStylePatterns is the "## Acceptance" / "### Acceptance" heading
	// family: exactly "Acceptance" (optional trailing colon), captured until
	// the next heading of the same or higher level.
	houseStylePatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?im)^##[ \t]+acceptance:?[ \t]*\r?\n([\s\S]*?)(?:\n#{1,2}[ \t]|\z)`),
		regexp.MustCompile(`(?im)^###[ \t]+acceptance:?[ \t]*\r?\n([\s\S]*?)(?:\n#{1,3}[ \t]|\z)`),
	}

	markdownPatterns = append([]*regexp.Regexp{
		regexp.MustCompile(`(?i)### acceptance criteria\s*\n([\s\S]*?)(?:\n###|\z)`),
		regexp.MustCompile(`(?i)### criteria\s*\n([\s\S]*?)(?:\n###|\z)`),
		regexp.MustCompile(`(?i)## acceptance criteria\s*\n([\s\S]*?)(?:\n##|\z)`),
	}, houseStylePatterns...)
)

// MarkdownPatterns returns a copy of the shared markdown heading patterns,
// so adapters with extra patterns can append to them and call ExtractWith.
// It includes HouseStylePatterns; adapters that need only that family must
// use HouseStylePatterns rather than slicing this list by position.
func MarkdownPatterns() []*regexp.Regexp {
	return append([]*regexp.Regexp(nil), markdownPatterns...)
}

// HouseStylePatterns returns a copy of the "## Acceptance" / "### Acceptance"
// heading patterns, a named subset of MarkdownPatterns.
func HouseStylePatterns() []*regexp.Regexp {
	return append([]*regexp.Regexp(nil), houseStylePatterns...)
}

// Extract returns the acceptance criteria listed under the first matching
// markdown acceptance heading in body (checkbox items, else plain "- " items).
func Extract(body string) []string {
	return ExtractWith(body, markdownPatterns, CheckboxItemRe, ListItemRe)
}

// ExtractWith is Extract with caller-supplied heading patterns and item
// regexps. The first pattern that matches body wins; checkbox is applied to
// the captured section, and list is used only when no checkbox item was found.
// Each regexp must capture the item text in group 1.
func ExtractWith(body string, patterns []*regexp.Regexp, checkbox, list *regexp.Regexp) []string {
	var criteria []string
	for _, pattern := range patterns {
		matches := pattern.FindStringSubmatch(body)
		if len(matches) <= 1 {
			continue
		}
		for _, item := range checkbox.FindAllStringSubmatch(matches[1], -1) {
			if len(item) > 1 {
				criteria = append(criteria, strings.TrimSpace(item[1]))
			}
		}
		if len(criteria) == 0 {
			for _, item := range list.FindAllStringSubmatch(matches[1], -1) {
				if len(item) > 1 {
					criteria = append(criteria, strings.TrimSpace(item[1]))
				}
			}
		}
		break
	}
	return criteria
}
