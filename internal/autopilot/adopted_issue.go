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
//     #N) when that issue is open. Adoption only sees open PRs, so "closed by
//     this PR's merge" cannot hold yet and a closed target is skipped;
//  2. the fix issue the durable spawned-fix claim or the borrowed-branch
//     registry names for this PR/branch, only when that issue is open and
//     still carries the autopilot-fix label;
//  3. the number parsed from the branch name.
//
// The registry read is destructive and always happens, so an entry whose fix
// issue turned out to be closed or unlabeled is dropped here rather than left
// to mis-attribute a later PR. The winning source is logged.
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
	var fixIssue int
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
		if fi, ok := c.borrowedBranchLookup.FixIssueForBranch(pr.Head.Ref); ok && fi > 0 && fi != branchIssue && fixIssue == 0 {
			fixIssue, fixSource = fi, adoptSourceBorrowedBranch
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
			if ok, _ := c.adoptionTargetOpen(ctx, pr, refs[0], false); ok {
				return won(adoptedIssue{Number: refs[0], Source: adoptSourceClosingReference, ViaFixIssue: refs[0] == fixIssue})
			}
		}
	}

	if fixIssue > 0 {
		ok, why := c.adoptionTargetOpen(ctx, pr, fixIssue, true)
		if ok {
			return won(adoptedIssue{Number: fixIssue, Source: fixSource, ViaFixIssue: true})
		}
		c.log.Info("reconciler: ignoring dead fix issue for adopted PR — registry entry dropped",
			"pr", pr.Number, "branch", pr.Head.Ref, "fix_issue", fixIssue, "source", fixSource, "reason", why)
		c.forgetBorrowedBranchForIssue(fixIssue)
	}

	return won(adoptedIssue{Number: branchIssue, Source: adoptSourceBranch})
}

// adoptionTargetOpen reports whether issue n may receive an adopted PR: it
// must be open and, for a fix issue, still carry the autopilot-fix label. A
// GetIssue error counts as usable (the pre-GH-5927 behaviour was to trust the
// candidate), and is logged. why names the reason when not.
func (c *Controller) adoptionTargetOpen(ctx context.Context, pr *github.PullRequest, n int, requireFixLabel bool) (ok bool, why string) {
	issue, err := c.ghClient.GetIssue(ctx, c.owner, c.repo, n)
	if err != nil {
		c.log.Warn("reconciler: failed to verify issue state for adopted PR, trusting it",
			"pr", pr.Number, "issue", n, "error", err)
		return true, ""
	}
	if issue == nil {
		return true, ""
	}
	if strings.EqualFold(issue.State, github.StateClosed) {
		return false, "issue is closed"
	}
	if requireFixLabel {
		for _, l := range issue.Labels {
			if strings.EqualFold(l.Name, labelAutopilotFix) {
				return true, ""
			}
		}
		return false, "issue no longer carries the " + labelAutopilotFix + " label"
	}
	return true, ""
}
