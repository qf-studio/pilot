package executor

import (
	"fmt"
	"strings"

	"github.com/qf-studio/pilot/internal/text"
)

// TASK-620 L2: the pre-PR verdict as a machine-parseable PR-body section.
// Autopilot (L5) and the post-merge sampler (L6) read it back with
// ParsePrePRReviewSection, so the heading and the "Key: value" lines below are a
// contract: change them together with the parser.

// prePRReviewHeading is the H2 heading that opens the section.
const prePRReviewHeading = "## Pre-PR review"

// PrePRFixNone is the Fix line value when no fix round ran. Otherwise the line
// carries the round's ReviewFixStatus.
const PrePRFixNone = "none"

const (
	prePRMaxDefects    = 30
	prePRMaxFieldChars = 1000
	// prePRMaxLocationChars matches maxPostMergeLocationChars in autopilot.
	prePRMaxLocationChars = 200
)

// prePRField bounds and cleans one model-produced field for a single markdown
// line: invisible Unicode stripped, backticks and newlines flattened, and a
// field over max cut at a sentence or word end with a "(truncated)" marker.
func prePRField(s string, max int) string {
	s, _ = text.SanitizeUntrusted(s)
	s = strings.Join(strings.Fields(strings.ReplaceAll(s, "`", "")), " ")
	return cutAtBoundary(s, max)
}

// RenderPrePRReviewSection renders the "Pre-PR review" section for the PR body,
// led by a blank line so it can be appended to a body. It returns "" when there
// is no verdict to show (review off, skipped, failed, unknown or exhausted).
//
// Decision: the Not-verified list is a plain "Not-verified:" line and not a
// "## Not verified" heading — LiveSmokeNotVerifiedBullets finds that heading
// by substring (it also matches "### Not verified") and would hold the PR for
// live-smoke wording in the reviewer's list.
func RenderPrePRReviewSection(out *PrePRReviewOutcome) string {
	if out == nil || out.Review == nil || out.Verdict == "" {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "\n\n%s\n\n", prePRReviewHeading)
	fmt.Fprintf(&sb, "Mode: %s\n", prePRField(out.Mode, 40))
	fmt.Fprintf(&sb, "Verdict: %s\n", out.Verdict)
	fix := PrePRFixNone
	if out.Fix != nil && out.Fix.Status != "" {
		fix = string(out.Fix.Status)
	}
	fmt.Fprintf(&sb, "Fix: %s\n", fix)

	if defects := out.Review.Defects; len(defects) > 0 {
		sb.WriteString("\nDefects:\n")
		for i, d := range defects {
			if i == prePRMaxDefects {
				fmt.Fprintf(&sb, "- … %d more defects omitted\n", len(defects)-prePRMaxDefects)
				break
			}
			tag := ""
			if d.Severity != "" {
				tag = "[" + prePRField(string(d.Severity), 20) + "] "
			}
			fmt.Fprintf(&sb, "- %sat %s", tag, prePRField(d.Location, prePRMaxLocationChars))
			// GH-5996: a defect with no claim prints its location alone, not a
			// dangling dash.
			if claim := prePRField(d.Claim, prePRMaxFieldChars); !postMergeNoClaim(claim) {
				fmt.Fprintf(&sb, " — %s", claim)
			}
			sb.WriteString("\n")
		}
	}

	// GH-5996: the first MaxPostMergeNotVerified entries are listed and one extra
	// bullet counts the rest, from the lines actually written.
	var notVerified []string
	hidden, untraced := 0, 0
	for _, raw := range out.Review.NotVerified {
		item := prePRField(raw, prePRMaxFieldChars)
		switch {
		case item == "":
		case len(notVerified) < MaxPostMergeNotVerified:
			notVerified = append(notVerified, item)
		default:
			hidden++
			if IsUntracedNotVerified(raw) {
				untraced++
			}
		}
	}
	if len(notVerified) > 0 {
		sb.WriteString("\nNot-verified:\n")
		for _, item := range notVerified {
			fmt.Fprintf(&sb, "- %s\n", item)
		}
		sb.WriteString(postMergeNotVerifiedCountLine(hidden, untraced))
	}
	// A killed fix session is mentioned on its own line: the parser ignores lines
	// it does not know.
	if out.Fix != nil && out.Fix.Terminated > 0 {
		fmt.Fprintf(&sb, "\nReview fix round was terminated after %ds; its edits were discarded.\n", int(out.Fix.Terminated.Seconds()))
	}
	return strings.TrimRight(sb.String(), "\n")
}

// ParsePrePRReviewSection reads the section RenderPrePRReviewSection wrote back
// out of a PR body: the review mode, the verdict and the Fix outcome. ok is
// false when the body has no section or the section lacks a mode or verdict.
// Like LiveSmokeNotVerifiedBullets it parses the body, not run state, so a
// section survives a body edit or a round trip through GitHub.
func ParsePrePRReviewSection(prBody string) (mode string, verdict PostMergeVerdict, fix string, ok bool) {
	lines := strings.Split(prBody, "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimRight(line, " \t\r") == prePRReviewHeading {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return "", "", "", false
	}
	for _, line := range lines[start:] {
		if strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "# ") {
			break
		}
		key, val, found := strings.Cut(strings.TrimSpace(line), ":")
		if !found {
			continue
		}
		val = strings.TrimSpace(val)
		switch key {
		case "Mode":
			if mode == "" {
				mode = val
			}
		case "Verdict":
			if verdict == "" {
				verdict = PostMergeVerdict(val)
			}
		case "Fix":
			if fix == "" {
				fix = val
			}
		}
	}
	if mode == "" || !verdict.Postable() {
		return "", "", "", false
	}
	return mode, verdict, fix, true
}
