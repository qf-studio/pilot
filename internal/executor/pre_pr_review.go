package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/memory/graphrecall"
)

// TASK-620 L1: pre-PR review. After the quality gates and before the push, the
// post-merge reviewer's checklist runs over the full diff inside a read-only
// checkout of the worktree HEAD. In shadow mode it records a verdict and acts
// on nothing; when its mode is not off it already replaces the self-review
// phase and the in-run intent judge.

// prePRMinWindow is the least finalize time a review needs to be worth starting.
const prePRMinWindow = 2 * time.Minute

// prePRMemoryLimit is how many ranked memories a review receives, as in the
// post-merge review.
const prePRMemoryLimit = 5

// PrePRReviewer is the review pass the executor runs before the push;
// *PostMergeReviewer satisfies it.
type PrePRReviewer interface {
	Review(ctx context.Context, in PostMergeReviewInput) (*PostMergeReview, error)
}

// PrePRReviewOutcome is what one pre-PR review pass left behind.
type PrePRReviewOutcome struct {
	// Mode is the pre_pr_review mode the run saw.
	Mode string
	// Verdict is the reviewer's verdict; empty (no verdict) when the review was
	// skipped, failed, or came back unknown or exhausted.
	Verdict PostMergeVerdict
	// Review is the parsed review; nil when the reviewer did not run or failed.
	Review *PostMergeReview
	// Skipped is why the review did not run; empty when it did.
	Skipped string
	// Err is the reviewer's error, if it ran and failed.
	Err error
	// Duration is the wall-clock time of the review call.
	Duration time.Duration
	// FilesRead is the number of files the reviewer opened in the checkout.
	FilesRead int
	// Passes is the number of reviewer passes the review ran; 0 when no review ran.
	Passes int
	// Fix is the fix round started on this verdict (TASK-620 L3); nil when none
	// applied. The verdict itself is never changed by it.
	Fix *ReviewFixOutcome
	// FixRounds counts the fix sessions started, checked against max_fix_rounds.
	FixRounds int
}

// defectCounts returns the blocking and minor defect counts of the review.
func (o *PrePRReviewOutcome) defectCounts() (blocking, minor int) {
	if o.Review == nil {
		return 0, 0
	}
	blocking = len(o.Review.BlockingDefects())
	return blocking, len(o.Review.Defects) - blocking
}

// verdictLabel is the verdict for logs and events; "none" when there is none.
func (o *PrePRReviewOutcome) verdictLabel() string {
	if o.Verdict == "" {
		return "none"
	}
	return string(o.Verdict)
}

var prePRDiffHeaderRe = regexp.MustCompile(`(?m)^diff --git a/(?:.+) b/(.+)$`)

// prePRDiffFiles lists the files a unified diff touches, in order, once each.
func prePRDiffFiles(diff string) []string {
	var files []string
	seen := map[string]bool{}
	for _, m := range prePRDiffHeaderRe.FindAllStringSubmatch(diff, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			files = append(files, m[1])
		}
	}
	return files
}

// prePRGateResults renders the gate results for the reviewer, one line per
// gate: name, pass or fail, duration.
func prePRGateResults(gates *QualityGatesResult) string {
	if gates == nil || len(gates.Gates) == 0 {
		return ""
	}
	lines := make([]string, 0, len(gates.Gates))
	for _, g := range gates.Gates {
		status := "passed"
		if !g.Passed {
			status = "failed"
		}
		lines = append(lines, fmt.Sprintf("%s: %s (%s)", g.Name, status, g.Duration.Round(time.Millisecond)))
	}
	return strings.Join(lines, "\n")
}

// runPrePRReview runs the pre-PR review once the gates have passed. It never
// fails the run: every failure is recorded on the outcome and the run goes on
// to push. It returns nil when the mode is off. Shadow mode never starts a fix
// round and writes nothing to GitHub.
func (r *Runner) runPrePRReview(ctx context.Context, task *Task, executionPath string, git *GitOperations, state *progressState, gates *QualityGatesResult) *PrePRReviewOutcome {
	mode := r.config.PrePRReviewMode()
	if mode == PrePRReviewModeOff {
		return nil
	}
	log := r.log.With(slog.String("task_id", task.ID))
	out := &PrePRReviewOutcome{Mode: mode}

	skip := func(reason string) *PrePRReviewOutcome {
		out.Skipped = reason
		log.Info("Pre-PR review skipped", slog.String("mode", mode), slog.String("reason", reason))
		r.recordPrePRReviewEvent(task, out)
		return out
	}

	complexity := DetectComplexity(task)
	switch {
	case r.prePRReviewer == nil:
		return skip("reviewer not initialized")
	case !task.CreatePR:
		return skip("create_pr=false")
	case task.DirectCommit:
		return skip("direct commit")
	case task.Branch == "":
		return skip("no branch")
	case git == nil:
		return skip("no git helper")
	case (gates == nil || !gates.AllPassed) && r.qualityCheckerFactory != nil:
		return skip("quality gates did not pass")
	case r.config.PrePRReview.SkipsComplexity(complexity):
		return skip(fmt.Sprintf("complexity %s", complexity))
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < prePRMinWindow {
		return skip("less than 2m left in the finalize window")
	}

	baseBranch := task.BaseBranch
	if baseBranch == "" {
		baseBranch, _ = git.GetDefaultBranch(ctx)
		if baseBranch == "" {
			baseBranch = "main"
		}
	}
	diff, _, err := git.GetDiffAgainstOrigin(ctx, baseBranch)
	if err != nil {
		return skip(fmt.Sprintf("diff unavailable: %v", err))
	}
	if strings.TrimSpace(diff) == "" {
		return skip("empty diff")
	}
	// A resolved SHA, never the literal HEAD: the review checkout wants a commit.
	sha, err := git.GetCurrentCommitSHA(ctx)
	if err != nil {
		return skip(fmt.Sprintf("head unavailable: %v", err))
	}

	var memories []graphrecall.MemorySummary
	if r.config.MemoryInjection != nil && r.config.MemoryInjection.Enabled {
		text := task.Title + " " + task.Description + " " + strings.Join(prePRDiffFiles(diff), " ")
		memories = graphrecall.RecallRelevant(executionPath, text, prePRMemoryLimit)
	}

	reviewCtx, cancel := context.WithTimeout(ctx, r.config.PrePRReview.EffectiveTimeout())
	defer cancel()

	r.reportProgress(task.ID, "Pre-PR Review", 96, "Reviewing the change against the system...")
	start := time.Now()
	review, err := r.prePRReviewer.Review(reviewCtx, PostMergeReviewInput{
		PreMerge:       true,
		IssueTitle:     task.Title,
		IssueBody:      task.Description,
		Diff:           diff,
		CIResult:       prePRGateResults(gates),
		Memories:       memories,
		ProjectPath:    executionPath,
		MergeCommitSHA: sha,
	})
	out.Duration = time.Since(start)
	switch {
	case err != nil:
		out.Err = err
	case review != nil:
		out.Review = review
		out.FilesRead = review.FilesRead
		out.Passes = review.EffectivePasses()
		// unknown and exhausted reviews followed no contract: no verdict.
		if review.Verdict.Postable() {
			out.Verdict = review.Verdict
			RecordPrePRReview(out.Verdict)
		}
	}

	blocking, minor := out.defectCounts()
	attrs := []any{
		slog.String("mode", mode),
		slog.String("verdict", out.verdictLabel()),
		slog.Int("blocking", blocking),
		slog.Int("minor", minor),
		slog.Int("files_read", out.FilesRead),
		slog.Int("passes", out.Passes),
		slog.Duration("duration", out.Duration),
	}
	if out.Err != nil {
		attrs = append(attrs, slog.Any("error", out.Err))
	}
	log.Info("Pre-PR review", attrs...)
	r.recordPrePRReviewEvent(task, out)
	return out
}

// recordPrePRReviewEvent writes the pre_pr_review execution event for out.
func (r *Runner) recordPrePRReviewEvent(task *Task, out *PrePRReviewOutcome) {
	blocking, minor := out.defectCounts()
	detail := struct {
		Mode       string `json:"mode"`
		Verdict    string `json:"verdict"`
		Blocking   int    `json:"blocking"`
		Minor      int    `json:"minor"`
		FilesRead  int    `json:"files_read"`
		Passes     int    `json:"passes,omitempty"`
		DurationMS int64  `json:"duration_ms"`
		Skipped    string `json:"skipped,omitempty"`
		Error      string `json:"error,omitempty"`
	}{out.Mode, out.verdictLabel(), blocking, minor, out.FilesRead, out.Passes, out.Duration.Milliseconds(), out.Skipped, ""}
	if out.Err != nil {
		detail.Error = out.Err.Error()
	}
	b, err := json.Marshal(detail)
	if err != nil {
		return
	}
	r.recordExecutionEvent(task.LogExecutionID(), memory.StagePrePRReview, string(b))
}

// PrePRReviewRCLabel is the PR label a REQUEST-CHANGES pre-PR verdict adds.
const PrePRReviewRCLabel = "pilot-review-rc"

// prePRReviewID is the pr_reviews key of a pre-PR review: the negated 62-bit
// FNV-64 of the execution id plus ":pre-pr".
//
// Decision: review_id is the table's primary key and normally a GitHub review
// id; post-merge rows negate their comment id. A pre-PR review has no GitHub
// object, so the key is derived from the execution instead. Negative, so it can
// never collide with a real (positive) review id; masked to 62 bits so it is
// safe wherever an int64 id is expected.
func prePRReviewID(executionID string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(executionID + ":pre-pr"))
	id := int64(h.Sum64() & (1<<62 - 1))
	if id == 0 {
		id = 1
	}
	return -id
}

// publishPrePRReview makes the verdict visible once the PR exists: the
// pilot-review-rc label on a REQUEST-CHANGES, and a pr_reviews row under
// PrePRReviewerID. It never fails the run. Shadow mode adds no other effect, and
// a PR URL without a PR number (a GitLab MR) gets neither.
func (r *Runner) publishPrePRReview(ctx context.Context, task *Task, projectPath, prURL string, out *PrePRReviewOutcome) {
	if out == nil || out.Verdict == "" {
		return
	}
	log := r.log.With(slog.String("task_id", task.ID))
	prNumber, ok := extractPRNumberFromURL(prURL)
	if !ok {
		return
	}

	if out.Verdict == PostMergeRequestChanges {
		if err := ghAddLabels(ctx, projectPath, strconv.Itoa(prNumber), []string{PrePRReviewRCLabel}); err != nil {
			log.Warn("Pre-PR review: failed to label PR", slog.Int("pr", prNumber), slog.Any("error", err))
		}
	}

	execID := task.LogExecutionID()
	if r.logStore == nil || execID == "" {
		return
	}
	rec := &memory.PRReview{
		ReviewID:    prePRReviewID(execID),
		ProjectPath: task.ProjectPath,
		PRNumber:    prNumber,
		ExecutionID: execID,
		Reviewer:    PrePRReviewerID,
		State:       "COMMENTED",
		Verdict:     LedgerVerdict(out.Verdict),
		SubmittedAt: time.Now(),
	}
	if err := r.logStore.UpsertPRReview(rec); err != nil {
		log.Warn("Pre-PR review: ledger upsert failed", slog.Int("pr", prNumber), slog.Any("error", err))
	}
}
