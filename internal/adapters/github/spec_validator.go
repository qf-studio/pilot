package github

import (
	"fmt"
	"regexp"
	"strings"
)

// SpecCommentMarker is embedded in the first-strike comment to enable idempotency checks.
const SpecCommentMarker = "<!-- pilot-spec-incomplete -->"

const specMinBodyLen = 100

var (
	// parentOnlyRe matches a body that is solely a "Parent: GH-NNN" line.
	parentOnlyRe = regexp.MustCompile(`(?i)^\s*Parent:\s*GH-\d+\s*$`)
	// parentRefRe extracts the parent issue number from a Parent: GH-NNN line.
	parentRefRe = regexp.MustCompile(`(?i)Parent:\s*GH-(\d+)`)
	// autopilotMetaRe detects decomposer-generated sub-issues.
	autopilotMetaRe = regexp.MustCompile(`<!--\s*autopilot-meta\s`)
	// sectionHeaderRe requires at least one recognized structural section header.
	sectionHeaderRe = regexp.MustCompile(`(?im)^##\s+(Acceptance|Implementation|Context|Background|Approach|Design|Refs)\b`)
)

// SpecValidationResult holds the outcome of ValidateSpec.
type SpecValidationResult struct {
	Valid          bool
	FailureReasons []string // human-readable; e.g. "body too short (78 chars, need 100)"
	SkipReason     string   // non-empty when the check was opted out
}

// ValidateSpec checks whether issue body is sufficiently specified for dispatch.
// parentResolver, if non-nil, is called to fetch a parent issue by number when
// evaluating decomposer-generated sub-issues.
func ValidateSpec(issue *Issue, parentResolver func(int) (*Issue, error)) SpecValidationResult {
	// Opt-out: pilot-skip-spec-check label bypasses all rules.
	for _, l := range issue.Labels {
		if l.Name == LabelSkipSpecCheck {
			return SpecValidationResult{
				Valid:      true,
				SkipReason: fmt.Sprintf("opted out via %s label", LabelSkipSpecCheck),
			}
		}
	}

	body := strings.TrimSpace(issue.Body)

	// Sub-issue with autopilot-meta marker: if parent is well-specced the child passes.
	if parentResolver != nil && autopilotMetaRe.MatchString(issue.Body) {
		if m := parentRefRe.FindStringSubmatch(issue.Body); len(m) > 1 {
			var parentNum int
			if _, err := fmt.Sscanf(m[1], "%d", &parentNum); err == nil && parentNum > 0 {
				if parent, err := parentResolver(parentNum); err == nil {
					parentResult := ValidateSpec(parent, nil)
					if parentResult.Valid || parentResult.SkipReason != "" {
						return SpecValidationResult{
							Valid:      true,
							SkipReason: fmt.Sprintf("parent GH-%d passes spec check", parentNum),
						}
					}
				}
			}
		}
	}

	var reasons []string

	if len(body) < specMinBodyLen {
		reasons = append(reasons, fmt.Sprintf("body too short (%d chars, need %d)", len(body), specMinBodyLen))
	}

	if parentOnlyRe.MatchString(body) {
		reasons = append(reasons, "body contains only a parent reference line")
	}

	if !sectionHeaderRe.MatchString(body) {
		reasons = append(reasons,
			"no structural section header (need one of: ## Acceptance, ## Implementation, ## Context, ## Background, ## Approach, ## Design, ## Refs)")
	}

	return SpecValidationResult{
		Valid:          len(reasons) == 0,
		FailureReasons: reasons,
	}
}
