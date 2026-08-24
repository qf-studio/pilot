package ghissue

import (
	"fmt"
	"regexp"
	"strings"

	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// SpecCommentMarker is the legacy (pre-fingerprint) form of the marker
// comment, kept around so tests and any historical comments that predate
// GH-4632 are still recognized as markers (see FindSpecCommentMarkerFingerprint).
const SpecCommentMarker = "<!-- pilot-spec-incomplete -->"

// specCommentMarkerRe matches a spec-incomplete marker comment and, when
// present, captures the sha256 fingerprint of the issue body at the time the
// marker was posted (GH-4632). Markers written before the fingerprint was
// introduced match with an empty capture group and are treated as
// stale/legacy by callers.
var specCommentMarkerRe = regexp.MustCompile(`<!--\s*pilot-spec-incomplete(?:\s+sha256=([0-9a-f]{64}))?\s*-->`)

// BuildSpecCommentMarker renders the marker comment for a first-strike
// comment, embedding a sha256 fingerprint of the issue body. A later guard
// pass can then tell a genuine repeat failure (same body, still thin) from a
// body that was edited since the last strike, without needing any state
// outside the comment itself.
func BuildSpecCommentMarker(bodyFingerprint string) string {
	return fmt.Sprintf("<!-- pilot-spec-incomplete sha256=%s -->", bodyFingerprint)
}

// FindSpecCommentMarkerFingerprint scans a comment body for the
// pilot-spec-incomplete marker. ok is false when no marker is present at
// all. When ok is true, an empty fingerprint means a legacy marker (posted
// before fingerprints existed) — callers should treat that as stale and
// equivalent to a first strike, not a confirmed repeat.
func FindSpecCommentMarkerFingerprint(commentBody string) (fingerprint string, ok bool) {
	m := specCommentMarkerRe.FindStringSubmatch(commentBody)
	if m == nil {
		return "", false
	}
	return m[1], true
}

const specMinBodyLen = 100

var (
	// parentOnlyRe matches a body that is solely a "Parent: GH-NNN" line.
	parentOnlyRe = regexp.MustCompile(`(?i)^\s*Parent:\s*GH-\d+\s*$`)
	// parentRefRe extracts the parent issue number from a Parent: GH-NNN line.
	parentRefRe = regexp.MustCompile(`(?i)Parent:\s*GH-(\d+)`)
	// autopilotMetaRe detects decomposer-generated sub-issues.
	autopilotMetaRe = regexp.MustCompile(`<!--\s*autopilot-meta\s`)
	// sectionHeaderRe requires at least one recognized structural section header (H2–H6).
	sectionHeaderRe = regexp.MustCompile(`(?im)^#{2,6}\s+(Acceptance|Implementation|Context|Background|Approach|Design|Refs)\b`)
)

// SpecValidationResult holds the outcome of ValidateSpec.
type SpecValidationResult struct {
	Valid          bool
	FailureReasons []string // human-readable; e.g. "body too short (78 chars, need 100)"
	SkipReason     string   // non-empty when the check was opted out
}

// ValidateSpec checks whether the issue body is sufficiently specified for
// dispatch. parentResolver, if non-nil, is called to fetch a parent issue by
// number when evaluating decomposer-generated sub-issues.
func ValidateSpec(issue *github.Issue, parentResolver func(int) (*github.Issue, error)) SpecValidationResult {
	// Opt-out: pilot-skip-spec-check label bypasses all rules.
	for _, l := range issue.Labels {
		if l.Name == github.LabelSkipSpecCheck {
			return SpecValidationResult{
				Valid:      true,
				SkipReason: fmt.Sprintf("opted out via %s label", github.LabelSkipSpecCheck),
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
			"no structural section header (need one of Acceptance, Implementation, Context, Background, Approach, Design, or Refs as an H2–H6 header — H1 is not accepted; only the heading text is checked, and it must match one of these words exactly in English — the body content underneath the heading may be written in any language)")
	}

	return SpecValidationResult{
		Valid:          len(reasons) == 0,
		FailureReasons: reasons,
	}
}
