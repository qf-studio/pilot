package autopilot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	ghadapter "github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/memory/graphrecall"
	"github.com/qf-studio/pilot/internal/text"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
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

// SetPostMergeMemoryInjection sets whether reviews recall project memories.
// The daemon passes executor.memory_injection.enabled so the one switch gates
// executor prompts and review context alike; a controller left unset recalls.
func (c *Controller) SetPostMergeMemoryInjection(enabled bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.postMergeMemoriesOff = !enabled
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

	c.mu.RLock()
	memories := !c.postMergeMemoriesOff
	c.mu.RUnlock()

	gatherCtx, cancel := context.WithTimeout(ctx, postMergeReviewGatherTimeout)
	gathered, err := gatherPostMergeReviewInput(gatherCtx, c.ghClient, c.owner, c.repo, c.projectPath, in.prNumber, in.issueNumber, memories,
		func(ctx context.Context, mergeSHA string) (string, string, error) {
			diff, err := c.postMergeDiffFor(ctx, mergeSHA)
			return diff, PostMergeDiffSourceLocal, err
		})
	cancel()
	if err != nil {
		c.metrics.RecordPostMergeReview(postMergeReviewError)
		c.log.Warn("post-merge review: gathering inputs failed",
			"pr", in.prNumber, "issue", in.issueNumber, "error", err, "duration", time.Since(start))
		return
	}

	input := gathered.Input
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
		"memories", review.Memories,
		"checkout", review.Checkout,
		"files_read", review.FilesRead,
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

// BuildPostMergeReviewComment renders the PR comment the controller posts for a
// review, so other callers post exactly the same text.
func BuildPostMergeReviewComment(review *executor.PostMergeReview) string {
	return buildPostMergeReviewComment(review)
}

// PostMergeDiffLine renders the "Diff: ..." line of a review.
func PostMergeDiffLine(review *executor.PostMergeReview) string { return postMergeDiffLine(review) }

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
	memories := fmt.Sprintf(", memories: %d", review.Memories)
	if review.Checkout {
		memories += fmt.Sprintf(", checkout: yes, files read: %d", review.FilesRead)
	}
	if !review.DiffTrimmed {
		return line + "no" + memories
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
	return line + fmt.Sprintf("yes (%d files: %s)", len(review.TrimmedFiles), list) + memories
}

// Diff sources reported by PostMergeGather.DiffSource.
const (
	PostMergeDiffSourceLocal         = "local checkout"
	PostMergeDiffSourceGitHubCompare = "github compare"
)

// PostMergeGitHub is the GitHub surface the gather step needs. The studio-sdk
// *github.Client satisfies it, which is what the controller passes.
type PostMergeGitHub interface {
	GetPullRequest(ctx context.Context, owner, repo string, number int) (*github.PullRequest, error)
	GetIssue(ctx context.Context, owner, repo string, number int) (*github.Issue, error)
}

// PostMergeClosingIssues is an optional capability of a PostMergeGitHub:
// listing the issues a PR closes. Without it, issue resolution skips straight
// to the head-branch name.
type PostMergeClosingIssues interface {
	ClosingIssueNumbers(ctx context.Context, owner, repo string, prNumber int) ([]int, error)
}

// PostMergeCompareDiff is an optional capability of a PostMergeGitHub: the
// diff of a merge commit against its first parent, read through GitHub. It is
// the diff source when there is no local checkout holding the commit.
type PostMergeCompareDiff interface {
	MergeCommitDiff(ctx context.Context, owner, repo, mergeSHA string) (string, error)
}

// PostMergeGather is the outcome of gathering one merged PR's review inputs.
type PostMergeGather struct {
	Input executor.PostMergeReviewInput
	// IssueNumber is the issue the review is judged against, whether the
	// caller gave it or it was resolved.
	IssueNumber int
	// MergeCommitSHA is the merge commit whose diff was read.
	MergeCommitSHA string
	// DiffSource says where the diff came from (PostMergeDiffSource*), with
	// the reason when the local checkout was tried and failed.
	DiffSource string
}

// postMergeBranchIssueRe finds the issue number in a head branch name such as
// "pilot/GH-5910".
var postMergeBranchIssueRe = regexp.MustCompile(`(?i)\bGH-(\d+)`)

// GatherPostMergeReviewInput collects what the post-merge reviewer reads about
// one merged PR: the PR, its merged diff and the issue it was meant to
// implement. issueNumber <= 0 resolves the issue from the PR's first closing
// issue reference, else the GH-N in its head branch name, else fails asking for
// the issue. The diff comes from the local checkout at projectPath (fetching
// the merge commit first) and, when that is unset or fails, from GitHub's
// compare of the merge commit's first parent to the merge commit when gh
// supports it (PostMergeCompareDiff). It also recalls the project's ranked
// knowledge-graph memories for the issue and the touched files into
// Input.Memories, unless disabled with WithoutMemoryInjection. It writes
// nothing anywhere.
func GatherPostMergeReviewInput(ctx context.Context, gh PostMergeGitHub, owner, repo, projectPath string, prNumber, issueNumber int, opts ...GatherOption) (*PostMergeGather, error) {
	cfg := gatherConfig{memories: true}
	for _, o := range opts {
		o(&cfg)
	}
	return gatherPostMergeReviewInput(ctx, gh, owner, repo, projectPath, prNumber, issueNumber, cfg.memories, nil)
}

// GatherOption tunes GatherPostMergeReviewInput.
type GatherOption func(*gatherConfig)

type gatherConfig struct{ memories bool }

// WithoutMemoryInjection skips the memory recall; callers pass it when
// executor.memory_injection.enabled is off, which gates review context too.
func WithoutMemoryInjection() GatherOption { return func(c *gatherConfig) { c.memories = false } }

// postMergeMemoryLimit is how many ranked memories a review receives.
const postMergeMemoryLimit = 5

var postMergeDiffHeaderRe = regexp.MustCompile(`(?m)^diff --git a/(?:.+) b/(.+)$`)

// postMergeDiffFiles lists the files a unified diff touches, from its
// "diff --git" headers, in order and without repeats.
func postMergeDiffFiles(diff string) []string {
	var files []string
	seen := map[string]bool{}
	for _, m := range postMergeDiffHeaderRe.FindAllStringSubmatch(diff, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			files = append(files, m[1])
		}
	}
	return files
}

// recallPostMergeMemories returns the ranked project memories relevant to the
// issue and the files the diff touches. It fails open: no project path, no
// graph, no match or any error yields nil, logged at Debug at most.
func recallPostMergeMemories(projectPath, issueTitle, issueBody, diff string) []graphrecall.MemorySummary {
	if projectPath == "" {
		return nil
	}
	text := issueTitle + " " + issueBody + " " + strings.Join(postMergeDiffFiles(diff), " ")
	memories := graphrecall.RecallRelevant(projectPath, text, postMergeMemoryLimit)
	slog.Debug("post-merge review: recalled project memories", "project", projectPath, "count", len(memories))
	if len(memories) == 0 {
		return nil
	}
	return memories
}

// gatherPostMergeReviewInput is GatherPostMergeReviewInput with a seam for the
// diff: the controller supplies its own (test override, local checkout only),
// nil uses the default local-then-GitHub order.
func gatherPostMergeReviewInput(ctx context.Context, gh PostMergeGitHub, owner, repo, projectPath string, prNumber, issueNumber int, memories bool,
	diffFn func(ctx context.Context, mergeSHA string) (diff, source string, err error)) (*PostMergeGather, error) {
	pr, err := gh.GetPullRequest(ctx, owner, repo, prNumber)
	if err != nil {
		return nil, fmt.Errorf("fetch PR #%d: %w", prNumber, err)
	}
	if pr.State == "open" && !pr.Merged {
		return nil, fmt.Errorf("PR #%d is still open: only merged PRs can be reviewed", prNumber)
	}
	if pr.MergeCommitSHA == "" {
		return nil, fmt.Errorf("merged PR has no merge commit sha")
	}

	if issueNumber <= 0 {
		issueNumber, err = resolvePostMergeIssue(ctx, gh, owner, repo, pr, prNumber)
		if err != nil {
			return nil, err
		}
	}

	if diffFn == nil {
		diffFn = func(ctx context.Context, mergeSHA string) (string, string, error) {
			return defaultPostMergeDiff(ctx, gh, owner, repo, projectPath, mergeSHA)
		}
	}
	diff, source, err := diffFn(ctx, pr.MergeCommitSHA)
	if err != nil {
		return nil, err
	}

	iss, err := gh.GetIssue(ctx, owner, repo, issueNumber)
	if err != nil {
		return nil, fmt.Errorf("fetch issue: %w", err)
	}
	var recalled []graphrecall.MemorySummary
	if memories {
		recalled = recallPostMergeMemories(projectPath, iss.Title, iss.Body, diff)
	}
	input := executor.PostMergeReviewInput{
		PRNumber:   prNumber,
		IssueTitle: iss.Title,
		IssueBody:  iss.Body,
		Diff:       diff,
		PRBody:     pr.Body,
		Memories:   recalled,
	}
	// GH-5916: a diff read from the local checkout means the merge commit is
	// in it, so the reviewer can open counterpart files there. A diff from
	// GitHub's compare keeps the diff-only review.
	if source == PostMergeDiffSourceLocal {
		input.ProjectPath = projectPath
		input.MergeCommitSHA = pr.MergeCommitSHA
	}
	return &PostMergeGather{
		Input:          input,
		IssueNumber:    issueNumber,
		MergeCommitSHA: pr.MergeCommitSHA,
		DiffSource:     source,
	}, nil
}

// resolvePostMergeIssue finds the issue a PR implements when the caller did
// not name one: the first closing issue reference, else the GH-N in the head
// branch name.
func resolvePostMergeIssue(ctx context.Context, gh PostMergeGitHub, owner, repo string, pr *github.PullRequest, prNumber int) (int, error) {
	var lookupErr error
	if ci, ok := gh.(PostMergeClosingIssues); ok {
		nums, err := ci.ClosingIssueNumbers(ctx, owner, repo, prNumber)
		if err != nil {
			lookupErr = err
		} else if len(nums) > 0 {
			return nums[0], nil
		}
	}
	if m := postMergeBranchIssueRe.FindStringSubmatch(pr.Head.Ref); len(m) == 2 {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
			return n, nil
		}
	}
	msg := fmt.Sprintf("PR #%d has no closing issue reference and its head branch %q carries no GH-N: pass the issue with --issue", prNumber, pr.Head.Ref)
	if lookupErr != nil {
		msg += fmt.Sprintf(" (closing-issue lookup failed: %v)", lookupErr)
	}
	return 0, errors.New(msg)
}

// defaultPostMergeDiff reads the merged diff from the local checkout when
// projectPath is set, else — or when the checkout cannot produce it — from
// GitHub's compare of the merge commit's first parent to the merge commit.
func defaultPostMergeDiff(ctx context.Context, gh PostMergeGitHub, owner, repo, projectPath, mergeSHA string) (diff, source string, err error) {
	var localErr error
	if projectPath != "" {
		diff, localErr = localMergedDiff(ctx, projectPath, mergeSHA)
		if localErr == nil {
			return diff, PostMergeDiffSourceLocal, nil
		}
	} else {
		localErr = errors.New("no project path configured")
	}
	cd, ok := gh.(PostMergeCompareDiff)
	if !ok {
		return "", "", localErr
	}
	diff, err = cd.MergeCommitDiff(ctx, owner, repo, mergeSHA)
	if err != nil {
		return "", "", fmt.Errorf("local diff: %v; github compare: %w", localErr, err)
	}
	return diff, fmt.Sprintf("%s (local checkout unavailable: %v)", PostMergeDiffSourceGitHubCompare, localErr), nil
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

// PostMergeGitHubClient is the GitHub client of an offline review: the
// studio-sdk client for the PR and issue, plus the in-tree client for the two
// reads the SDK does not expose (closing issue references and the raw compare
// diff). It satisfies PostMergeGitHub, PostMergeClosingIssues and
// PostMergeCompareDiff, and AddComment for --post.
type PostMergeGitHubClient struct {
	*github.Client
	extra *ghadapter.Client
}

// NewPostMergeGitHubClient pairs an SDK client with an in-tree client.
func NewPostMergeGitHubClient(sdk *github.Client, extra *ghadapter.Client) *PostMergeGitHubClient {
	return &PostMergeGitHubClient{Client: sdk, extra: extra}
}

// ClosingIssueNumbers lists the issues the PR closes.
func (c *PostMergeGitHubClient) ClosingIssueNumbers(ctx context.Context, owner, repo string, prNumber int) ([]int, error) {
	return c.extra.ClosingIssueNumbers(ctx, owner, repo, prNumber)
}

// MergeCommitDiff returns GitHub's diff of the merge commit's first parent to
// the merge commit.
func (c *PostMergeGitHubClient) MergeCommitDiff(ctx context.Context, owner, repo, mergeSHA string) (string, error) {
	parent, err := c.extra.GetCommitFirstParent(ctx, owner, repo, mergeSHA)
	if err != nil {
		return "", fmt.Errorf("first parent of %s: %w", mergeSHA, err)
	}
	return c.extra.CompareDiff(ctx, owner, repo, parent, mergeSHA)
}
