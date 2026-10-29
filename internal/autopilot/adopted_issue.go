package autopilot

import (
	"context"
	"fmt"
	"strings"

	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// labelAutopilotFix marks a CI-fix issue autopilot filed for a PR (GH-5927).
// It is one of the default IssueLabels (types.go); a fix issue that lost it
// was taken out of autopilot's hands and no longer owns its branch.
const labelAutopilotFix = "autopilot-fix"

// Sources a reconciler adoption can bind an untracked PR to an issue by.
const (
	adoptSourceClosingReference = "closing-reference"
	adoptSourceSpawnedFixClaim  = "spawned-fix-claim"
	adoptSourceBorrowedBranch   = "borrowed-branch-registry"
	adoptSourceBranch           = "branch"
)

// adoptedIssue is the issue resolveAdoptedIssueNumber bound an untracked PR to.
type adoptedIssue struct {
	// Number is the issue to register the PR under; 0 when the branch name
	// carries no issue number and nothing else named one.
	Number int
	// Source says which rule produced Number (adoptSource*).
	Source string
	// BranchIssue is the issue number parsed from the head branch (0 if none).
	BranchIssue int
	// ViaFixIssue is true when Number is a fix issue running on a borrowed
	// origin branch, so the PR must register through OnPRCreatedForFixIssue
	// and keep the registry re-record on a persist failure (GH-5430).
	ViaFixIssue bool
}

// resolveAdoptedIssueNumber decides which issue a reconciler adopts an
// untracked open pilot/GH-N PR under — GH-5927. In order:
//
//  1. the first closing-issue reference in the PR body (Closes/Fixes/Resolves
//     #N) when that issue is verifiably open. Adoption only sees open PRs, so
//     "closed by this PR's merge" cannot hold yet and a closed target is
//     skipped;
//  2. the fix issue the durable spawned-fix claim or the borrowed-branch
//     registry names for this PR/branch, only when that issue is verifiably
//     open and still carries the autopilot-fix label;
//  3. the number parsed from the branch name.
//
// A target whose state cannot be read (rate limit, 5xx) is never bound: the
// rule is "open and labeled", and an unverified issue is neither. The registry
// read is destructive and always happens, so an entry whose fix issue turned
// out to be closed or unlabeled is dropped here rather than left to
// mis-attribute a later PR, while an entry for a live or unverifiable fix
// issue that did not win is put back so the fix issue's own PR still
// attributes. The winning source is logged.
func (c *Controller) resolveAdoptedIssueNumber(ctx context.Context, pr *github.PullRequest) adoptedIssue {
	var branchIssue int
	if _, err := fmt.Sscanf(pr.Head.Ref, "pilot/GH-%d", &branchIssue); err != nil {
		c.log.Warn("reconciler: failed to parse branch name", "branch", pr.Head.Ref, "error", err)
		branchIssue = 0
	}

	// GH-5413: a fix/revision issue runs on its ORIGIN issue's branch, so a
	// PR on pilot/GH-O may belong to fix issue F. The durable spawned-fix
	// claim survives restarts; if that lookup fails, say so and try the
	// registry.
	var fixIssue, fixFromPR int
	fixSource := ""
	if c.stateStore != nil {
		if fi, err := c.stateStore.HasSpawnedFixForPR(c.repoKey(), pr.Number); err != nil {
			c.log.Warn("reconciler: spawned-fix lookup failed, will try the borrowed-branch registry",
				"pr", pr.Number, "branch", pr.Head.Ref, "branch_issue", branchIssue, "error", err)
		} else if fi > 0 && fi != branchIssue {
			fixIssue, fixSource = fi, adoptSourceSpawnedFixClaim
		}
	}
	// GH-5421: the claim is keyed by the ORIGIN PR CI-fix spawning closed, so
	// it misses the usual flow (new PR on the same branch); the registry is
	// keyed by branch. FixIssueForBranch consumes its entry (GH-5425), so it
	// is called even when the claim already named a fix issue.
	if c.borrowedBranchLookup != nil {
		if fi, fromPR, ok := c.borrowedBranchLookup.ConsumeBorrowedBranch(pr.Head.Ref); ok && fi > 0 && fi != branchIssue && fixIssue == 0 {
			fixIssue, fixFromPR, fixSource = fi, fromPR, adoptSourceBorrowedBranch
		}
	}

	won := func(a adoptedIssue) adoptedIssue {
		a.BranchIssue = branchIssue
		c.log.Info("reconciler: resolved issue for adopted PR",
			"pr", pr.Number, "branch", pr.Head.Ref, "issue", a.Number, "source", a.Source,
			"branch_issue", branchIssue, "fix_issue", fixIssue)
		return a
	}

	// A memoized closed-origin skip means an earlier tick already found no
	// live target; don't spend a GetIssue per tick re-asking (GH-5425).
	_, memoized := c.closedOriginSkipDecision(pr.Number)
	if !memoized || fixIssue > 0 {
		if refs := closingRefs(pr.Body); len(refs) > 0 && refs[0] > 0 {
			// Only the first reference counts.
			state, why := c.adoptionTargetState(ctx, pr, refs[0], false)
			if state == targetLive {
				if fixIssue > 0 && fixIssue != refs[0] {
					if fixState, _ := c.adoptionTargetState(ctx, pr, fixIssue, true); fixState != targetDead {
						c.restoreBorrowedBranch(pr, fixIssue, fixFromPR, fixSource)
					} else {
						c.log.Info("reconciler: dead fix issue lost to the closing reference — registry entry dropped",
							"pr", pr.Number, "branch", pr.Head.Ref, "fix_issue", fixIssue, "source", fixSource)
					}
				}
				return won(adoptedIssue{Number: refs[0], Source: adoptSourceClosingReference, ViaFixIssue: refs[0] == fixIssue})
			}
			c.log.Info("reconciler: closing-reference target not usable for adopted PR",
				"pr", pr.Number, "issue", refs[0], "state", state, "reason", why)
		}
	}

	if fixIssue > 0 {
		state, why := c.adoptionTargetState(ctx, pr, fixIssue, true)
		switch state {
		case targetLive:
			return won(adoptedIssue{Number: fixIssue, Source: fixSource, ViaFixIssue: true})
		case targetDead:
			c.log.Info("reconciler: ignoring dead fix issue for adopted PR — registry entry dropped",
				"pr", pr.Number, "branch", pr.Head.Ref, "fix_issue", fixIssue, "source", fixSource, "reason", why)
			c.forgetBorrowedBranchForIssue(fixIssue)
		default:
			// Unverifiable: not bound, but the entry the read above consumed
			// is kept so a later PR can still resolve it.
			c.log.Warn("reconciler: fix issue state unverifiable for adopted PR — not binding it",
				"pr", pr.Number, "branch", pr.Head.Ref, "fix_issue", fixIssue, "source", fixSource, "reason", why)
			c.restoreBorrowedBranch(pr, fixIssue, fixFromPR, fixSource)
		}
	}

	return won(adoptedIssue{Number: branchIssue, Source: adoptSourceBranch})
}

// targetState is what adoptionTargetState learned about a candidate issue.
type targetState int

const (
	// targetLive: the issue is open (and, for a fix issue, labeled).
	targetLive targetState = iota
	// targetDead: the issue is closed or lost the autopilot-fix label.
	targetDead
	// targetUnknown: GitHub could not tell (lookup error or empty answer).
	targetUnknown
)

func (s targetState) String() string {
	switch s {
	case targetLive:
		return "live"
	case targetDead:
		return "dead"
	default:
		return "unknown"
	}
}

// adoptionTargetState reports whether issue n may receive an adopted PR: it
// must be open and, for a fix issue, still carry the autopilot-fix label. A
// GetIssue error or empty answer is targetUnknown, never targetLive — a closed
// or unlabeled issue whose lookup happens to fail must not win. why names the
// reason when the state is not live.
func (c *Controller) adoptionTargetState(ctx context.Context, pr *github.PullRequest, n int, requireFixLabel bool) (state targetState, why string) {
	issue, err := c.ghClient.GetIssue(ctx, c.owner, c.repo, n)
	if err != nil {
		c.log.Warn("reconciler: failed to verify issue state for adopted PR",
			"pr", pr.Number, "issue", n, "error", err)
		return targetUnknown, "issue lookup failed: " + err.Error()
	}
	if issue == nil {
		return targetUnknown, "issue lookup returned nothing"
	}
	if strings.EqualFold(issue.State, github.StateClosed) {
		return targetDead, "issue is closed"
	}
	if requireFixLabel {
		for _, l := range issue.Labels {
			if strings.EqualFold(l.Name, labelAutopilotFix) {
				return targetLive, ""
			}
		}
		return targetDead, "issue no longer carries the " + labelAutopilotFix + " label"
	}
	return targetLive, ""
}

// restoreBorrowedBranch puts back the registry entry FixIssueForBranch
// consumed for fixIssue when the PR was adopted under another issue. Only
// registry-sourced entries are restored; a spawned-fix claim is durable.
// fromPR is the origin PR number the consumed entry carried: the dispatch-time
// registration passes it (> 0), and githubOnPRCreatedHandler's BorrowedBranch
// gate requires fromPR > 0, so re-recording 0 would silently disarm the entry
// for the fix issue's own PR-created hook — GH-5937.
func (c *Controller) restoreBorrowedBranch(pr *github.PullRequest, fixIssue, fromPR int, source string) {
	if c.borrowedBranchLookup == nil || source != adoptSourceBorrowedBranch || fixIssue <= 0 {
		return
	}
	c.borrowedBranchLookup.RecordBorrowedBranch(fmt.Sprintf("GH-%d", fixIssue), pr.Head.Ref, fromPR)
}

// fixIssueStillAdoptable reports whether issue may still own a borrowed
// branch: open and carrying the autopilot-fix label — the same rule
// adoptionTargetState applies at adoption time.
func fixIssueStillAdoptable(issue *github.Issue) bool {
	if strings.EqualFold(issue.State, github.StateClosed) {
		return false
	}
	for _, l := range issue.Labels {
		if strings.EqualFold(l.Name, labelAutopilotFix) {
			return true
		}
	}
	return false
}

// forgetBorrowedBranchIfDead drops issue's borrowed-branch entry when the
// issue has been observed closed (by hand or otherwise) or unlabeled, so the
// entry does not wait for an adoption-time check to be cleaned up — GH-5937.
// A nil lookup or issue is a no-op.
func forgetBorrowedBranchIfDead(lookup BorrowedBranchLookup, issue *github.Issue) {
	if lookup == nil || issue == nil || issue.Number <= 0 || fixIssueStillAdoptable(issue) {
		return
	}
	lookup.ForgetBorrowedBranch(fmt.Sprintf("GH-%d", issue.Number))
}

// forgetBorrowedBranchIfDeadIssue is forgetBorrowedBranchIfDead over the
// controller's registry — GH-5937.
func (c *Controller) forgetBorrowedBranchIfDeadIssue(issue *github.Issue) {
	forgetBorrowedBranchIfDead(c.borrowedBranchLookup, issue)
}
