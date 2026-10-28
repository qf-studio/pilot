package autopilot

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/text"
)

// GH-5861: post-merge reviewer wiring. After a Pilot PR merges, handleMerged
// starts one model pass over the merged diff, judged against the issue. The
// verdict is posted on the PR, stored in the pr_reviews ledger, and — for
// REQUEST-CHANGES — filed as one labeled follow-up issue. The review is an
// input to human review of tier-1 merges, never a substitute, and never
// re-opens or reverts the merged PR.

// PostMergeReviewer runs one post-merge review. *executor.PostMergeReviewer
// satisfies it; tests substitute a fake.
type PostMergeReviewer interface {
	Review(ctx context.Context, in executor.PostMergeReviewInput) (*executor.PostMergeReview, error)
}

const (
	// postMergeReviewLabel marks follow-up issues filed by the reviewer.
	postMergeReviewLabel = "post-merge-review"

	// postMergeReviewNoDecomposeLabel keeps the executor's epic detection from
	// splitting the follow-up issue into sub-issues: its four H2 sections plus
	// the defect list pass the 200-word threshold (GH-5527 class).
	postMergeReviewNoDecomposeLabel = "no-decompose"

	// postMergeReviewGatherTimeout bounds the GitHub and git calls that gather
	// a review's inputs; the model call has its own timeout (reviewer config).
	postMergeReviewGatherTimeout = 2 * time.Minute

	// Verdict label for reviews that never produced a verdict because the
	// subprocess or an input fetch failed.
	postMergeReviewError = "error"

	// These bounds keep a runaway model answer from producing a megabyte
	// comment or issue body.
	maxPostMergeDefects    = 30
	maxPostMergeFieldChars = 1000
	maxPostMergeNotesChars = 4000
)

// postMergeReviewBudget is the post-merge reviews' own daily counter. It is
// deliberately separate from the executor's daily ticket cap
// (executor.DailyCapGate): reviews write no executions row, so they neither
// count toward that cap nor are held by it, and a review spike cannot starve
// ticket dispatch.
//
// Decision: the day is the UTC calendar day and the count is in-memory. A
// daemon restart can therefore grant up to one extra budget in a day; the
// per-PR PostMergeReviewPosted flag is what persists, so a restart never
// re-reviews a PR, and the cap exists to bound model spend, not to meter it
// exactly.
type postMergeReviewBudget struct {
	mu        sync.Mutex
	cap       int
	now       func() time.Time // test seam; time.Now when nil
	day       string
	used      int
	capLogged bool
}

// PostMergeReviewBudget is the exported name of the review budget so the
// daemon wiring can build one and share it across controllers.
type PostMergeReviewBudget = postMergeReviewBudget

// NewPostMergeReviewBudget returns a budget allowing cap reviews per UTC day.
func NewPostMergeReviewBudget(cap int) *PostMergeReviewBudget {
	return &postMergeReviewBudget{cap: cap}
}

// acquire takes one review from today's budget. ok is false when the budget is
// spent; firstDenial is true only for the first denial of the day so the
// caller logs "cap reached" once rather than once per skipped PR.
func (b *postMergeReviewBudget) acquire() (ok, firstDenial bool, day string, used int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	if b.now != nil {
		now = b.now()
	}
	day = now.UTC().Format("2006-01-02")
	if day != b.day {
		b.day, b.used, b.capLogged = day, 0, false
	}
	if b.used >= b.cap {
		first := !b.capLogged
		b.capLogged = true
		return false, first, day, b.used
	}
	b.used++
	return true, false, day, b.used
}

// SetPostMergeReviewer wires the post-merge reviewer (GH-5861). Nil, or a
// config with post_merge_review disabled, leaves handleMerged untouched.
func (c *Controller) SetPostMergeReviewer(r PostMergeReviewer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.postMergeReviewer = r
}

// SetPostMergeReviewBudget installs a review budget shared with other
// controllers. One autopilot controller runs per project, so a per-controller
// budget would make the real daily bound N times the configured cap; the
// daemon builds one budget and hands it to every controller. Nil keeps the
// lazy per-controller fallback of postMergeBudget.
func (c *Controller) SetPostMergeReviewBudget(b *PostMergeReviewBudget) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.postMergeReviewBudget = b
}

// PostMergeReviewBudget returns the controller's review budget (the shared one
// when wired, else its lazily built own).
func (c *Controller) PostMergeReviewBudget() *PostMergeReviewBudget { return c.postMergeBudget() }

// waitPostMergeReviews blocks until every started review goroutine finishes.
func (c *Controller) waitPostMergeReviews() { c.postMergeReviewWG.Wait() }

// postMergeReviewInputs is the per-PR data a review goroutine needs, copied
// out of the live PRState so the goroutine never reads it concurrently.
type postMergeReviewInputs struct {
	prNumber    int
	prURL       string
	issueNumber int
	ciStatus    CIStatus
}

// maybeStartPostMergeReview launches the post-merge review for a merged PR
// when the knob is on. It is called from handleMerged and is a no-op unless
// the reviewer is wired, the PR came from a GitHub issue (the review is
// judged against it), the PR has not been reviewed yet (PostMergeReviewPosted,
// persisted so a restart does not repeat it) and today's budget has room.
func (c *Controller) maybeStartPostMergeReview(ctx context.Context, prState *PRState) {
	if !c.config.PostMergeReviewEnabled() {
		return
	}
	c.mu.RLock()
	reviewer := c.postMergeReviewer
	c.mu.RUnlock()
	if reviewer == nil {
		return
	}
	if prState.IssueNumber <= 0 {
		c.log.Debug("post-merge review skipped: PR has no GitHub issue to review against", "pr", prState.PRNumber)
		return
	}
	if prState.PostMergeReviewPosted {
		return
	}

	budget := c.postMergeBudget()
	ok, firstDenial, day, used := budget.acquire()
	if !ok {
		if firstDenial {
			c.log.Warn("post-merge review daily cap reached, skipping reviews until the next UTC day",
				"pr", prState.PRNumber, "cap", budget.cap, "used", used, "day", day)
		}
		return
	}

	// Attempt-once: set and persist before the goroutine starts, so a re-entry
	// or a restart cannot start a second review for this PR.
	prState.PostMergeReviewPosted = true
	c.persistPRState(prState)

	in := postMergeReviewInputs{
		prNumber:    prState.PRNumber,
		prURL:       prState.PRURL,
		issueNumber: prState.IssueNumber,
		ciStatus:    prState.CIStatus,
	}
	// The stage handler's ctx is cancelled when the handler returns, which
	// would kill the review mid-flight; detach it but keep its values.
	reviewCtx := context.WithoutCancel(ctx)
	c.postMergeReviewWG.Add(1)
	go func() {
		defer c.postMergeReviewWG.Done()
		defer func() {
			if r := recover(); r != nil {
				c.log.Error("post-merge review panicked", "pr", in.prNumber, "panic", r)
			}
		}()
		c.runPostMergeReview(reviewCtx, reviewer, in)
	}()
}

// postMergeBudget returns the controller's review budget: the one installed by
// SetPostMergeReviewBudget, else one created from the configured daily cap on
// first use (tests, gateway mode with a single controller).
func (c *Controller) postMergeBudget() *postMergeReviewBudget {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.postMergeReviewBudget == nil {
		c.postMergeReviewBudget = &postMergeReviewBudget{cap: c.config.PostMergeReview.EffectiveDailyCap()}
	}
	return c.postMergeReviewBudget
}

// runPostMergeReview gathers a merged PR's review inputs, runs the reviewer
// and posts the outcome. Every failure is logged and counted, never returned:
// the PR has already merged and nothing here may affect its lifecycle.
func (c *Controller) runPostMergeReview(ctx context.Context, reviewer PostMergeReviewer, in postMergeReviewInputs) {
	start := time.Now()

	gatherCtx, cancel := context.WithTimeout(ctx, postMergeReviewGatherTimeout)
	pr, err := c.ghClient.GetPullRequest(gatherCtx, c.owner, c.repo, in.prNumber)
	var issueTitle, issueBody, diff string
	if err == nil {
		diff, err = c.postMergeDiffFor(gatherCtx, pr.MergeCommitSHA)
	}
	if err == nil {
		iss, ierr := c.ghClient.GetIssue(gatherCtx, c.owner, c.repo, in.issueNumber)
		if ierr != nil {
			err = fmt.Errorf("fetch issue: %w", ierr)
		} else {
			issueTitle, issueBody = iss.Title, iss.Body
		}
	}
	cancel()
	if err != nil {
		c.metrics.RecordPostMergeReview(postMergeReviewError)
		c.log.Warn("post-merge review: gathering inputs failed",
			"pr", in.prNumber, "issue", in.issueNumber, "error", err, "duration", time.Since(start))
		return
	}

	input := executor.PostMergeReviewInput{
		PRNumber:   in.prNumber,
		IssueTitle: issueTitle,
		IssueBody:  issueBody,
		Diff:       diff,
		PRBody:     pr.Body,
	}
	if in.ciStatus != "" {
		input.CIResult = fmt.Sprintf("CI status at merge time: %s", in.ciStatus)
	}

	review, err := reviewer.Review(ctx, input)
	if err != nil {
		c.metrics.RecordPostMergeReview(postMergeReviewError)
		c.log.Warn("post-merge review failed", "pr", in.prNumber, "error", err, "duration", time.Since(start))
		return
	}

	c.metrics.RecordPostMergeReview(string(review.Verdict))
	c.log.Info("post-merge review",
		"pr", in.prNumber,
		"verdict", string(review.Verdict),
		"defects", len(review.Defects),
		"duration", time.Since(start),
	)
	if review.Verdict == executor.PostMergeUnknown {
		// Output broke the contract: post nothing rather than guess.
		return
	}

	c.postPostMergeReview(ctx, in, review)
}

// postPostMergeReview posts the verdict comment, records it in the ledger and,
// for REQUEST-CHANGES, files the follow-up issue.
func (c *Controller) postPostMergeReview(ctx context.Context, in postMergeReviewInputs, review *executor.PostMergeReview) {
	var commentID int64
	// A PR is an issue for comment purposes: AddComment takes the PR number.
	if cm, err := c.ghClient.AddComment(ctx, c.owner, c.repo, in.prNumber, buildPostMergeReviewComment(review)); err != nil {
		c.log.Warn("post-merge review: failed to post PR comment", "pr", in.prNumber, "error", err)
	} else if cm != nil {
		commentID = cm.ID
	}

	c.recordPostMergeReview(in, review, commentID)

	if review.Verdict != executor.PostMergeRequestChanges {
		return
	}
	issueNum, err := c.feedbackLoop.CreatePostMergeReviewIssue(ctx, in.prNumber, in.prURL, in.issueNumber, review)
	if err != nil {
		c.log.Warn("post-merge review: failed to file follow-up issue", "pr", in.prNumber, "error", err)
		return
	}
	c.log.Info("post-merge review: follow-up issue filed", "pr", in.prNumber, "issue", issueNum)
}

// recordPostMergeReview upserts the verdict into pr_reviews so bench
// defect-rate tracking sees it (reviewer id post-merge-reviewer).
//
// Decision: pr_reviews.review_id is the primary key and is normally a GitHub
// review id. This review is an issue comment, whose id space can overlap, so
// the row is keyed on the negated comment id: unique per comment across
// repos, and it can never collide with a real (positive) review id.
func (c *Controller) recordPostMergeReview(in postMergeReviewInputs, review *executor.PostMergeReview, commentID int64) {
	ledger := c.reviewLedger()
	if ledger == nil {
		return
	}
	if commentID <= 0 {
		c.log.Warn("post-merge review: not recording in ledger, no comment id", "pr", in.prNumber)
		return
	}
	var executionID string
	if exec, err := c.memoryStore.GetLatestExecutionByTaskID(fmt.Sprintf("GH-%d", in.issueNumber), c.projectPath); err == nil && exec != nil {
		executionID = exec.ID
	}
	rec := &memory.PRReview{
		ReviewID:    -commentID,
		ProjectPath: c.projectPath,
		PRNumber:    in.prNumber,
		ExecutionID: executionID,
		Reviewer:    executor.PostMergeReviewerID,
		State:       "COMMENTED",
		Verdict:     ledgerVerdict(review.Verdict),
		SubmittedAt: time.Now(),
	}
	if err := ledger.UpsertPRReview(rec); err != nil {
		c.log.Warn("post-merge review: ledger upsert failed", "pr", in.prNumber, "error", err)
	}
}

// ledgerVerdict maps the reviewer's verdict onto the pr_reviews vocabulary.
func ledgerVerdict(v executor.PostMergeVerdict) string {
	switch v {
	case executor.PostMergeApprove:
		return memory.VerdictApprove
	case executor.PostMergeApproveWithNotes:
		return memory.VerdictApproveWNotes
	case executor.PostMergeRequestChanges:
		return memory.VerdictRequestChanges
	}
	return ""
}

// sanitizePostMergeField strips invisible Unicode and bounds a model-produced
// field. The reviewer reads attacker-influenceable text (issue and PR bodies,
// diff content) and its output lands in a `pilot`-labeled issue that Pilot
// itself will execute, so it is treated as untrusted on the way out.
func sanitizePostMergeField(s string, max int) string {
	s, _ = text.SanitizeUntrusted(s)
	s = strings.TrimSpace(s)
	if len(s) > max {
		s = strings.ToValidUTF8(s[:max], "") + "…"
	}
	return s
}

// plainPostMergeField is sanitizePostMergeField without backticks.
func plainPostMergeField(s string, max int) string {
	return strings.ReplaceAll(sanitizePostMergeField(s, max), "`", "")
}

// formatPostMergeDefects renders the defect list as markdown bullets.
//
// Decision: locations are plain text ("at internal/foo/bar.go:42") and
// backticks are stripped from every field. The list lands in a `pilot` issue,
// and the executor's base-presence gate holds an issue whose backticked path
// is not on the task repo's default branch (SOP Rule 3b) — the checklist asks
// the model to name counterpart files that may live in another repository.
func formatPostMergeDefects(defects []executor.PostMergeDefect) string {
	var sb strings.Builder
	for i, d := range defects {
		if i == maxPostMergeDefects {
			fmt.Fprintf(&sb, "- … %d more defects omitted\n", len(defects)-maxPostMergeDefects)
			break
		}
		fmt.Fprintf(&sb, "- at %s — %s", plainPostMergeField(d.Location, 200), plainPostMergeField(d.Claim, maxPostMergeFieldChars))
		if ev := plainPostMergeField(d.Evidence, maxPostMergeFieldChars); ev != "" {
			fmt.Fprintf(&sb, "\n  Evidence: %s", ev)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// buildPostMergeReviewComment renders the PR comment: verdict first, then
// defects and notes.
func buildPostMergeReviewComment(review *executor.PostMergeReview) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "**VERDICT: %s**\n\n", review.Verdict)
	if len(review.Defects) > 0 {
		sb.WriteString("### Defects\n\n")
		sb.WriteString(formatPostMergeDefects(review.Defects))
		sb.WriteString("\n")
	}
	if notes := sanitizePostMergeField(review.Notes, maxPostMergeNotesChars); notes != "" {
		sb.WriteString("### Notes\n\n")
		sb.WriteString(notes)
		sb.WriteString("\n\n")
	}
	if review.DiffBytes > 0 || review.DiffFiles > 0 {
		sb.WriteString(postMergeDiffLine(review))
		sb.WriteString("\n\n")
	}
	sb.WriteString("<sub>Automated post-merge review — an input to human review of this merge, not a substitute for it.</sub>")
	return sb.String()
}

// maxPostMergeDiffLineFiles caps the trimmed paths named in the Diff line.
const maxPostMergeDiffLineFiles = 10

// postMergeDiffLine reports how much of the diff the reviewer saw, so a reader
// can tell a trimmed review from a thorough one.
func postMergeDiffLine(review *executor.PostMergeReview) string {
	line := fmt.Sprintf("Diff: %d bytes, %d files, trimmed: ", review.DiffBytes, review.DiffFiles)
	if !review.DiffTrimmed {
		return line + "no"
	}
	files := review.TrimmedFiles
	extra := 0
	if len(files) > maxPostMergeDiffLineFiles {
		extra = len(files) - maxPostMergeDiffLineFiles
		files = files[:maxPostMergeDiffLineFiles]
	}
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, plainPostMergeField(f, 200))
	}
	list := strings.Join(names, ", ")
	if extra > 0 {
		list += fmt.Sprintf(", and %d more", extra)
	}
	return line + fmt.Sprintf("yes (%d files: %s)", len(review.TrimmedFiles), list)
}

// postMergeDiffFor returns the merged diff of mergeSHA. It uses the
// controller's override when set (tests), else the local checkout.
func (c *Controller) postMergeDiffFor(ctx context.Context, mergeSHA string) (string, error) {
	if mergeSHA == "" {
		return "", fmt.Errorf("merged PR has no merge commit sha")
	}
	if c.postMergeDiff != nil {
		return c.postMergeDiff(ctx, mergeSHA)
	}
	return localMergedDiff(ctx, c.projectPath, mergeSHA)
}

// localMergedDiff diffs mergeSHA against its first parent in the project's
// checkout, which is the whole PR for both squash and merge-commit merges.
// The commit is fetched first when the checkout does not have it yet; fetching
// only adds objects, it never moves a branch or touches the worktree.
func localMergedDiff(ctx context.Context, projectPath, mergeSHA string) (string, error) {
	if projectPath == "" {
		return "", fmt.Errorf("no project path to diff in")
	}
	git := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", projectPath}, args...)...)
		return cmd.CombinedOutput()
	}
	if _, err := git("rev-parse", "--verify", "--quiet", mergeSHA+"^{commit}"); err != nil {
		if out, ferr := git("fetch", "--quiet", "origin", mergeSHA); ferr != nil {
			return "", fmt.Errorf("fetch merge commit %s: %w: %s", mergeSHA, ferr, strings.TrimSpace(string(out)))
		}
	}
	out, err := git("diff", "--no-color", "--no-ext-diff", mergeSHA+"^1", mergeSHA)
	if err != nil {
		return "", fmt.Errorf("git diff %s^1 %s: %w: %s", mergeSHA, mergeSHA, err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
