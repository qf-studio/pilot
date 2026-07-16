package autopilot

import (
	"context"
	"fmt"
	"sort"

	"github.com/qf-studio/pilot/internal/memory"
)

// maxReleaseBackfillTags bounds how many of the most recent tags
// earliestReleaseTagContaining inspects. GitHub's tags endpoint returns
// newest-first and this reconciliation only ever targets recent release-train
// residue (GH-4370), so 100 (GitHub's per-page max, and ListTags's only
// supported page) comfortably covers the window without unbounded pagination.
const maxReleaseBackfillTags = 100

// reconcileReleaseBackfill is GH-4370's periodic release-ledger reconciliation.
// A manual tag push (bypassing the automated release train entirely) leaves
// every PR physically contained in that release wedged in autopilot_pr_state
// at StageFailed or StageReleasing forever: RestoreState refuses to rehydrate
// StageFailed rows ("shouldn't be active"), and a scope carrier whose
// autopilot_scope_release row already resolved terminal is skipped too
// (GH-4331) — both classes are permanently invisible to the normal poll loop
// once orphaned. This sweep reads every persisted row directly from the state
// store (not c.activePRs, which the orphans were never rehydrated into) and
// heals any row whose PR did in fact merge and ship. Ground truth is git
// ancestry: a PR is released iff its merge commit is an ancestor of an
// existing release tag; the earliest such tag names the version.
func (c *Controller) reconcileReleaseBackfill(ctx context.Context) {
	if c.stateStore == nil || c.ghClient == nil {
		return
	}
	states, err := c.stateStore.LoadAllPRStates(c.repoKey())
	if err != nil {
		c.log.Warn("reconcileReleaseBackfill: failed to load PR states", "error", err)
		return
	}
	for _, prState := range states {
		if prState.Stage != StageFailed && prState.Stage != StageReleasing {
			continue
		}
		c.healReleaseBackfillRow(ctx, prState)
	}
}

// healReleaseBackfillRow resolves prState's live merge status and tag
// ancestry and, on a confirmed release match, backfills the execution ladder
// and drains the residue row — mirroring exactly what a successful
// handleReleasing does on the normal path, without re-running any of the
// tag-creation logic (the tag already exists; this is bookkeeping, not a new
// release).
func (c *Controller) healReleaseBackfillRow(ctx context.Context, prState *PRState) {
	owner, repo := prState.RepoOwnerAndName(c.owner, c.repo)

	ghPR, err := c.ghClient.GetPullRequest(ctx, owner, repo, prState.PRNumber)
	if err != nil {
		c.log.Debug("reconcileReleaseBackfill: failed to fetch PR, skipping",
			"pr", prState.PRNumber, "error", err)
		return
	}
	if !ghPR.Merged || ghPR.MergeCommitSHA == "" {
		// Genuinely unreleased (or never merged) — leave the row exactly as is.
		return
	}

	tag, err := c.earliestReleaseTagContaining(ctx, owner, repo, ghPR.MergeCommitSHA)
	if err != nil {
		c.log.Warn("reconcileReleaseBackfill: tag ancestry lookup failed",
			"pr", prState.PRNumber, "sha", ShortSHA(ghPR.MergeCommitSHA), "error", err)
		return
	}
	if tag == "" {
		// Merged, but not yet covered by any tag — genuinely unreleased.
		return
	}

	previousStage := prState.Stage
	c.recordReleaseBackfillEvent(prState, ghPR.MergeCommitSHA, tag)

	prState.ReleaseVersion = tag
	prState.HeadSHA = ghPR.MergeCommitSHA
	if prState.ScopeKey != "" {
		c.markScopeReleaseDone(prState, tag)
	}
	c.removePR(prState.PRNumber)

	c.log.Info("reconcileReleaseBackfill: healed merged-but-unreleased residue row",
		"pr", prState.PRNumber, "stage_was", previousStage, "version", tag,
		"sha", ShortSHA(ghPR.MergeCommitSHA))
}

// recordReleaseBackfillEvent writes the released execution event for
// prState, unless one is already recorded. Idempotent so a repeat sweep — or
// a crash between this write and the row-drain in healReleaseBackfillRow —
// never double-stamps the ladder (GH-4370, mirrors GH-4277's heal semantics:
// this only appends the missing terminal event, it never re-stamps the
// execution row's own timestamps to "now").
func (c *Controller) recordReleaseBackfillEvent(prState *PRState, mergeSHA, tag string) {
	if c.memoryStore == nil {
		return
	}
	taskID := fmt.Sprintf("GH-%d", prState.IssueNumber)
	if prState.IssueNumber == 0 {
		taskID = fmt.Sprintf("PR-%d", prState.PRNumber)
	}
	exec, err := c.memoryStore.GetLatestExecutionByTaskID(taskID, c.projectPath)
	if err != nil {
		c.log.Warn("reconcileReleaseBackfill: no execution row for task, skipping event",
			"pr", prState.PRNumber, "task_id", taskID, "error", err)
		return
	}
	already, err := c.memoryStore.HasExecutionEventStage(exec.ID, memory.StageReleased)
	if err != nil {
		c.log.Warn("reconcileReleaseBackfill: failed to check existing released event",
			"pr", prState.PRNumber, "execution_id", exec.ID, "error", err)
		return
	}
	if already {
		return
	}
	detail := fmt.Sprintf("release-backfill (GH-4370): pr #%d merge commit %s found in tag %s (manual tag push bypassed the release train)",
		prState.PRNumber, ShortSHA(mergeSHA), tag)
	if err := c.memoryStore.RecordExecutionEvent(exec.ID, memory.StageReleased, detail); err != nil {
		c.log.Warn("reconcileReleaseBackfill: failed to record released event",
			"pr", prState.PRNumber, "execution_id", exec.ID, "error", err)
	}
}

// earliestReleaseTagContaining returns the earliest (lowest-semver) release
// tag whose history contains sha, or "" if no tag among the most recent
// maxReleaseBackfillTags contains it. The earliest tag is the one that
// actually shipped the commit — a later tag also contains it by
// transitivity, but naming that one would misreport when the work released.
func (c *Controller) earliestReleaseTagContaining(ctx context.Context, owner, repo, sha string) (string, error) {
	tags, err := c.ghClient.ListTags(ctx, owner, repo, maxReleaseBackfillTags)
	if err != nil {
		return "", err
	}

	type versionedTag struct {
		name string
		sha  string
		ver  SemVer
	}
	versioned := make([]versionedTag, 0, len(tags))
	for _, tag := range tags {
		ver, err := ParseSemVer(tag.Name)
		if err != nil {
			continue // not a release tag — not a candidate
		}
		versioned = append(versioned, versionedTag{name: tag.Name, sha: tag.Commit.SHA, ver: ver})
	}
	sort.Slice(versioned, func(i, j int) bool {
		a, b := versioned[i].ver, versioned[j].ver
		if a.Major != b.Major {
			return a.Major < b.Major
		}
		if a.Minor != b.Minor {
			return a.Minor < b.Minor
		}
		return a.Patch < b.Patch
	})

	for _, t := range versioned {
		if t.sha == sha {
			return t.name, nil
		}
		status, err := c.ghClient.CompareStatus(ctx, owner, repo, sha, t.sha)
		if err != nil {
			return "", fmt.Errorf("compare %s against tag %s: %w", ShortSHA(sha), t.name, err)
		}
		if status == "ahead" || status == "identical" {
			return t.name, nil
		}
	}
	return "", nil
}
