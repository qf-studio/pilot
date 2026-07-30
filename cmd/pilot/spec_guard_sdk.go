package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"

	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"

	"github.com/qf-studio/pilot/internal/ghissue"
)

// applySpecGuardSDK runs the two-strike spec validation gate for an issue on
// the SDK poll path (M7 4d.3). Returns true when dispatch should be skipped.
//
// The marker comment embeds a sha256 fingerprint of the issue body
// (<!-- pilot-spec-incomplete sha256=... -->, GH-4632) so the guard can tell
// a genuine repeat failure from a body that was edited since the last
// strike, without any state outside the comment itself:
//   - no marker on the issue at all           -> first strike
//   - latest marker's fingerprint matches      -> second strike (same body,
//     still thin) -> escalate to pilot-blocked
//   - latest marker's fingerprint mismatches   -> body changed since the
//     last strike -> treat as a fresh first strike with new reasons
//   - latest marker predates fingerprints      -> stale/legacy -> treat as a
//     first strike
//
// Board-failed-column sync is handled at the SDK poller level, not here.
func applySpecGuardSDK(ctx context.Context, client *githubSDK.Client, owner, repo string, issue *githubSDK.Issue, reasons []string) bool {
	comments, err := client.ListIssueComments(ctx, owner, repo, issue.Number)
	if err != nil {
		slog.Warn("spec-guard(sdk): failed to list comments, skipping guard",
			slog.Int("issue", issue.Number), slog.Any("error", err))
		return false
	}

	// Walk in order and keep the last marker seen — if the guard has fired
	// more than once, the most recent comment reflects the last strike.
	var lastFingerprint string
	var lastMarkerFound bool
	for _, c := range comments {
		if fp, ok := ghissue.FindSpecCommentMarkerFingerprint(c.Body); ok {
			lastFingerprint = fp
			lastMarkerFound = true
		}
	}

	fingerprint := specBodyFingerprint(issue.Body)
	secondStrike := lastMarkerFound && lastFingerprint != "" && lastFingerprint == fingerprint

	if !secondStrike {
		// First strike (no prior marker, a mismatched fingerprint because the
		// body changed, or a legacy marker without one): warn and ask the
		// author to improve the body.
		if err := client.AddLabels(ctx, owner, repo, issue.Number, []string{githubSDK.LabelSpecIncomplete}); err != nil {
			logGitHubAPIError("AddLabels[spec-incomplete,sdk]", owner, repo, issue.Number, err)
		}
		comment := buildSpecIncompleteComment(reasons, fingerprint)
		if _, err := client.AddComment(ctx, owner, repo, issue.Number, comment); err != nil {
			logGitHubAPIError("AddComment[spec-incomplete,sdk]", owner, repo, issue.Number, err)
		}
		slog.Warn("spec-guard(sdk): first strike — issue body too thin, dispatch skipped",
			slog.Int("issue", issue.Number), slog.Any("reasons", reasons))
	} else {
		// Second strike: same body fingerprint as the last strike. Before
		// escalating, take one more fresh single-issue GET and re-validate —
		// mirrors the confirm-before-destructive-act pattern in
		// finalizeExternalClose (internal/autopilot/controller.go, GH-4570 /
		// #4572). A poll tick can lag a body edit that landed between the
		// first strike and now (GH-4498), and trusting the stale in-memory
		// snapshot here would block an issue the author already fixed.
		freshIssue, err := client.GetIssue(ctx, owner, repo, issue.Number)
		if err != nil {
			slog.Warn("spec-guard(sdk): failed to re-fetch issue before escalating, aborting strike",
				slog.Int("issue", issue.Number), slog.Any("error", err))
			return true
		}
		parentResolver := func(parentNum int) (*githubSDK.Issue, error) {
			return client.GetIssue(ctx, owner, repo, parentNum)
		}
		specResult := ghissue.ValidateSpec(freshIssue, parentResolver)
		if specResult.Valid || specResult.SkipReason != "" {
			slog.Info("spec-guard(sdk): body now passes spec validation on fresh re-read, aborting second-strike escalation",
				slog.Int("issue", issue.Number))
			return true
		}

		// Still failing on the fresh read: escalate. Post an escalation
		// comment (mirrors the first-strike branch above) before blocking,
		// so the block/unblock loop is visible on the issue thread instead
		// of only a daemon-side slog.Warn. The marker embeds the fresh
		// body's fingerprint (not the stale `fingerprint` computed from the
		// in-memory snapshot above) so a later guard pass compares against
		// what was actually judged here.
		freshFingerprint := specBodyFingerprint(freshIssue.Body)
		comment := buildSpecEscalationComment(specResult.FailureReasons, freshFingerprint, len(freshIssue.Body))
		if _, err := client.AddComment(ctx, owner, repo, issue.Number, comment); err != nil {
			logGitHubAPIError("AddComment[blocked,sdk]", owner, repo, issue.Number, err)
		}
		if err := client.AddLabels(ctx, owner, repo, issue.Number, []string{githubSDK.LabelBlocked}); err != nil {
			logGitHubAPIError("AddLabels[blocked,sdk]", owner, repo, issue.Number, err)
		}
		slog.Warn("spec-guard(sdk): second strike — escalating to pilot-blocked",
			slog.Int("issue", issue.Number), slog.Any("reasons", specResult.FailureReasons), slog.Int("body_len", len(freshIssue.Body)))
	}

	return true
}

// specBodyFingerprint returns the sha256 hex digest of the trimmed issue
// body. Used to detect whether the body changed between guard passes.
func specBodyFingerprint(body string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(body)))
	return hex.EncodeToString(sum[:])
}

// buildSpecIncompleteComment renders the structured first-strike comment,
// embedding the body fingerprint in the marker so a later guard pass can
// tell a genuine repeat failure from an edited-but-still-thin body.
func buildSpecIncompleteComment(reasons []string, bodyFingerprint string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", ghissue.BuildSpecCommentMarker(bodyFingerprint))
	fmt.Fprintf(&b, "⚠️ Pilot can't dispatch this issue: the spec body is too thin to execute against.\n\n")
	fmt.Fprintf(&b, "**What failed:**\n")
	for _, r := range reasons {
		fmt.Fprintf(&b, "- %s\n", r)
	}
	fmt.Fprintf(&b, "\n**Suggested template:**\n\n")
	fmt.Fprintf(&b, "```markdown\n")
	fmt.Fprintf(&b, "## Context\n\n<!-- Describe what this changes and why -->\n\n")
	fmt.Fprintf(&b, "## Acceptance\n\n- [ ] ...\n\n")
	fmt.Fprintf(&b, "## Implementation\n\n<!-- Optional: describe the approach -->\n")
	fmt.Fprintf(&b, "```\n\n")
	fmt.Fprintf(&b, "To retry: edit the issue body, then remove the `%s` label.\n", githubSDK.LabelSpecIncomplete)
	fmt.Fprintf(&b, "If `%s` was also added, remove that too.\n", githubSDK.LabelBlocked)
	return b.String()
}

// buildSpecEscalationComment renders the structured second-strike
// (escalation) comment, mirroring buildSpecIncompleteComment above. It
// carries the same fingerprinted marker so a later guard pass still finds
// it when scanning comment history, plus the failure reasons and observed
// body length from the fresh pre-escalation re-read, so the block/unblock
// loop is visible on the issue thread instead of only a daemon-side log.
func buildSpecEscalationComment(reasons []string, bodyFingerprint string, bodyLen int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", ghissue.BuildSpecCommentMarker(bodyFingerprint))
	fmt.Fprintf(&b, "🚫 Pilot is escalating this issue to `%s`: the spec body is still too thin after an earlier warning (body length: %d chars).\n\n", githubSDK.LabelBlocked, bodyLen)
	fmt.Fprintf(&b, "**What still fails:**\n")
	for _, r := range reasons {
		fmt.Fprintf(&b, "- %s\n", r)
	}
	fmt.Fprintf(&b, "\nTo retry: edit the issue body, then remove the `%s` label.\n", githubSDK.LabelBlocked)
	return b.String()
}
