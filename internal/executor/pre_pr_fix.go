package executor

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// TASK-620 L3: the pre-PR review fix round. In enforce mode a REQUEST-CHANGES
// with at least one blocking defect gets one fix attempt in the same
// conversation before the push, then one quality-gate re-check. A fix that fails
// its gates is rolled back to the SHA the round started from; the verdict stays
// REQUEST-CHANGES either way (a second review pass is out of scope).

const (
	// reviewFixMinRemaining is the least task time that must be left for a fix
	// round to start: a fix call plus its re-gate cannot finish in less.
	reviewFixMinRemaining = 12 * time.Minute
	// reviewFixRegateTimeout bounds the re-gate. It runs on a fresh context, like
	// the GH-4876 quality-gate retry pass, so a task deadline that blew during
	// the fix cannot doom the check.
	reviewFixRegateTimeout = 5 * time.Minute
	// reviewFixMaxListed caps the REVIEW_FIXED items kept on the outcome.
	reviewFixMaxListed = 10
)

// reviewFixGitTimeout bounds each HEAD read and reset of a fix round. It is a
// var so a test can shrink it below the duration of a fake fix session.
var reviewFixGitTimeout = selfReviewHygieneTimeout

// ReviewFixStatus is how a pre-PR fix round ended.
type ReviewFixStatus string

const (
	// ReviewFixFixed: the fix committed and passed the re-gate.
	ReviewFixFixed ReviewFixStatus = "fixed"
	// ReviewFixNoChange: the session left no commit (or said REVIEW_UNFIXABLE);
	// the tree was reset to PreFixSHA.
	ReviewFixNoChange ReviewFixStatus = "no_change"
	// ReviewFixSkippedCtx: the task context was already done; no call was made.
	ReviewFixSkippedCtx ReviewFixStatus = "skipped_ctx"
	// ReviewFixSkippedBudget: under reviewFixMinRemaining of task time was left.
	ReviewFixSkippedBudget ReviewFixStatus = "skipped_budget"
	// ReviewFixFailed: the fix session failed or was killed; the tree was reset.
	ReviewFixFailed ReviewFixStatus = "failed"
	// ReviewFixReverted: the fix failed its re-gate; HEAD was reset to PreFixSHA.
	ReviewFixReverted ReviewFixStatus = "reverted"
	// ReviewFixRegateError: the re-gate could not run; HEAD was reset to PreFixSHA.
	ReviewFixRegateError ReviewFixStatus = "regate_error"
)

// ReviewFixOutcome is what one pre-PR fix round left behind.
type ReviewFixOutcome struct {
	Status ReviewFixStatus
	// PreFixSHA is HEAD when the round started; empty when no call was made.
	PreFixSHA string
	// PostFixSHA is HEAD after the fix session; for reverted and regate_error it
	// is the commit that was rolled back.
	PostFixSHA string
	// Fixed lists what the session reported under REVIEW_FIXED:.
	Fixed []string
	// Elapsed is the wall-clock time of the round, fix call and re-gate included.
	Elapsed time.Duration
	// Terminated is how long the session ran when it was killed by its context,
	// else zero. The PR body mentions it.
	Terminated time.Duration
}

// reviewFixCtx derives the fix context from the task context: detached from the
// task's cancellation so the round is not cut short by a deadline that fires
// while the finalize windows are still open, bounded by min(fix_timeout,
// remaining), and still ended by an explicit cancel of the task (the
// propagation idiom of finalizeCtx).
func reviewFixCtx(taskCtx context.Context, fixTimeout time.Duration) (context.Context, context.CancelFunc) {
	budget := fixTimeout
	if deadline, ok := taskCtx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < budget {
			budget = remaining
		}
	}
	fctx, cancel := context.WithTimeout(context.WithoutCancel(taskCtx), budget)
	stop := context.AfterFunc(taskCtx, func() {
		if errors.Is(taskCtx.Err(), context.Canceled) {
			cancel()
		}
	})
	return fctx, func() {
		stop()
		cancel()
	}
}

// runReviewFix runs the fix round for a REQUEST-CHANGES pre-PR review and
// records it on outcome.Fix. It returns nil when the round does not apply:
// not enforce mode, no REQUEST-CHANGES verdict, no blocking defect, or
// max_fix_rounds already spent. It never fails the run: every failure is
// recorded on the outcome and the run goes on to push.
//
// taskCtx is the task's own context (before finalizeCtx replaced it) and
// taskCtxWasDone is whether it was already done when the backend returned; both
// say whether the task's own budget is gone, which the finalize window cannot.
func (r *Runner) runReviewFix(taskCtx context.Context, taskCtxWasDone bool, task *Task, executionPath string, git *GitOperations, state *progressState, outcome *PrePRReviewOutcome) *ReviewFixOutcome {
	if outcome == nil || outcome.Mode != PrePRReviewModeEnforce || outcome.Verdict != PostMergeRequestChanges || outcome.Review == nil || git == nil {
		return nil
	}
	var cfg *PrePRReviewConfig // nil-safe: the Effective* getters default
	if r.config != nil {
		cfg = r.config.PrePRReview
	}
	blocking := outcome.Review.BlockingDefects()
	if len(blocking) == 0 || outcome.FixRounds >= cfg.EffectiveMaxFixRounds() {
		return nil
	}
	log := r.log.With(slog.String("task_id", task.ID))
	fix := &ReviewFixOutcome{}
	outcome.Fix = fix
	start := time.Now()

	finish := func(status ReviewFixStatus) *ReviewFixOutcome {
		fix.Status = status
		fix.Elapsed = time.Since(start).Round(time.Millisecond)
		RecordPrePRFix(string(status))
		r.recordReviewFixEvent(task, fix)
		log.Info("Pre-PR review fix round",
			slog.String("status", string(status)),
			slog.String("pre_fix_sha", fix.PreFixSHA),
			slog.String("post_fix_sha", fix.PostFixSHA),
			slog.Int("fixed", len(fix.Fixed)),
			slog.Duration("elapsed", fix.Elapsed),
		)
		return fix
	}

	if taskCtxWasDone || taskCtx.Err() != nil {
		return finish(ReviewFixSkippedCtx)
	}
	if deadline, ok := taskCtx.Deadline(); ok && time.Until(deadline) < reviewFixMinRemaining {
		return finish(ReviewFixSkippedBudget)
	}

	preSHA, err := readReviewFixHead(git)
	if err != nil {
		// Without a SHA to roll back to, a fix cannot be undone: do not start one.
		log.Warn("Pre-PR review fix skipped: HEAD unreadable", slog.Any("error", err))
		return finish(ReviewFixFailed)
	}
	fix.PreFixSHA = preSHA

	outcome.FixRounds++

	fixCtx, fixCancel := reviewFixCtx(taskCtx, cfg.EffectiveFixTimeout())
	defer fixCancel()

	phase := selfReviewOutcome{ran: true}
	result, execErr := r.runReviewFixSession(fixCtx, task, executionPath, state, blocking)
	if execErr != nil {
		phase.failed = true
		phase.killed = selfReviewWasTerminated(fixCtx, execErr)
		if phase.killed {
			fix.Terminated = max(time.Since(start).Round(time.Second), time.Second)
		}
	} else if result != nil {
		fix.Fixed = parseReviewFixed(result.Output)
		phase.fixed = strings.Contains(result.Output, "REVIEW_FIXED:")
	}

	// The session edits the tree it runs in; leave it clean (or committed) in
	// worktree mode too, because the worktree is what gets pushed.
	r.reviewPhaseTreeHygiene(task, git, reviewFixPhase, phase)

	postSHA, headErr := readReviewFixHead(git)
	if headErr == nil {
		fix.PostFixSHA = postSHA
	}
	unfixable := execErr == nil && result != nil && !phase.fixed && strings.Contains(result.Output, "REVIEW_UNFIXABLE:")

	switch {
	case execErr != nil:
		// A failed or killed session may have committed part of a fix: roll it back.
		r.resetReviewFix(log, git, fix, headErr != nil || postSHA != preSHA)
		fix.Fixed = nil
		return finish(ReviewFixFailed)
	case headErr != nil:
		r.resetReviewFix(log, git, fix, true)
		return finish(ReviewFixFailed)
	case unfixable:
		r.resetReviewFix(log, git, fix, postSHA != preSHA)
		fix.Fixed = nil
		return finish(ReviewFixNoChange)
	case postSHA == preSHA:
		fix.Fixed = nil
		return finish(ReviewFixNoChange)
	}

	// G3: the fix moved HEAD, so it has to pass the gates again.
	if r.qualityCheckerFactory == nil || task.SkipQualityGates {
		log.Info("Pre-PR review fix kept without a re-gate: no quality gates configured")
		return finish(ReviewFixFixed)
	}
	checker := r.qualityCheckerFactory(task.ID, qualityLookupPath(task, executionPath), executionPath)
	regateCtx, regateCancel := context.WithTimeout(context.Background(), reviewFixRegateTimeout)
	gate, gateErr := checker.Check(regateCtx)
	regateCancel()
	switch {
	case gateErr != nil:
		log.Warn("Pre-PR review fix re-gate errored, reverting", slog.Any("error", gateErr))
		r.resetReviewFix(log, git, fix, true)
		fix.Fixed = nil
		return finish(ReviewFixRegateError)
	case gate == nil || !gate.Passed:
		log.Warn("Pre-PR review fix failed the quality gates, reverting")
		r.resetReviewFix(log, git, fix, true)
		fix.Fixed = nil
		return finish(ReviewFixReverted)
	}
	return finish(ReviewFixFixed)
}

// readReviewFixHead reads HEAD on a fresh context per call: the task context is
// usually the thing that is about to expire, and a context shared between the
// pre-fix and post-fix reads would be spent by the fix session itself (up to
// fix_timeout), failing the post-fix read and rolling back a valid fix.
func readReviewFixHead(git *GitOperations) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), reviewFixGitTimeout)
	defer cancel()
	return git.GetCurrentCommitSHA(ctx)
}

// resetReviewFix puts HEAD back on the SHA the round started from (G4). It does
// nothing when moved is false.
func (r *Runner) resetReviewFix(log *slog.Logger, git *GitOperations, fix *ReviewFixOutcome, moved bool) {
	if !moved {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), reviewFixGitTimeout)
	defer cancel()
	if err := git.ResetHardToCommit(ctx, fix.PreFixSHA); err != nil {
		log.Error("Pre-PR review fix: reset to the pre-fix SHA failed; the tree still carries the fix",
			slog.String("pre_fix_sha", fix.PreFixSHA), slog.Any("error", err))
	}
}

// runReviewFixSession makes the one backend call of the round: the original
// conversation resumed (when the backend allows), narrowed to the blocking
// defects, in the execution path.
func (r *Runner) runReviewFixSession(ctx context.Context, task *Task, executionPath string, state *progressState, blocking []PostMergeDefect) (*BackendResult, error) {
	r.reportProgress(task.ID, "Review Fix", 96, "Fixing blocking review defects...")

	complexity := DetectComplexity(task)
	selectedModel := r.resolveSelectedModel(task)
	selectedEffort := r.modelRouter.SelectEffort(task)

	var resumeSessionID string
	if r.config != nil && r.config.ClaudeCode != nil && r.config.ClaudeCode.UseSessionResume {
		resumeSessionID = state.sessionID
	}

	// Relayed through AlertEventTypeDeadManAttempt since this package cannot
	// import internal/alerts; the tracker is registered in cmd/pilot/poller_github.go.
	r.emitAlertEvent(AlertEvent{
		Type:      AlertEventTypeDeadManAttempt,
		TaskID:    task.ID,
		Metadata:  map[string]string{"tracker": ReviewFixDeadManTrackerName},
		Timestamp: time.Now(),
	})

	allowed, mcp := r.executionToolOptions()
	// Through backendExecute with the execution path: it enforces that the path
	// is not the shared project root (GH-4702 phantom re-implementation guard).
	result, err := r.backendExecute(ctx, task, executionPath, ExecuteOptions{
		Prompt:            r.buildReviewFixPrompt(blocking),
		TaskID:            task.ID,
		Verbose:           task.Verbose,
		Model:             selectedModel,
		Effort:            selectedEffort,
		ResumeSessionID:   resumeSessionID,
		LivenessPolicy:    ResolveLivenessPolicy(r.effectiveStallTimeout(), selectedEffort, complexity),
		AllowedTools:      allowed,
		MCPConfigPath:     mcp,
		SourceRepo:        task.SourceRepo,
		SourceIssueID:     task.SourceIssueID,
		Branch:            task.Branch,
		HeartbeatCallback: r.heartbeatTimeoutCallback(task),
		EventHandler: func(event BackendEvent) {
			state.tokensInput += event.TokensInput
			state.tokensOutput += event.TokensOutput
			state.cacheCreationInputTokens += event.CacheCreationInputTokens
			state.cacheReadInputTokens += event.CacheReadInputTokens
			state.addReportedCost(event.ReportedCostUSD)
			if event.Type == EventTypeToolResult && event.ToolResult != "" {
				extractCommitSHA(event.ToolResult, state)
			}
		},
	})
	r.ingestGhGuardDenials(task, result)

	if err != nil {
		r.log.Warn("Pre-PR review fix session failed", slog.String("task_id", task.ID), slog.Any("error", err))
		r.emitAlertEvent(AlertEvent{
			Type:      AlertEventTypeDeadManFailure,
			TaskID:    task.ID,
			Error:     err.Error(),
			Metadata:  map[string]string{"tracker": ReviewFixDeadManTrackerName},
			Timestamp: time.Now(),
		})
		return nil, err
	}
	r.emitAlertEvent(AlertEvent{
		Type:      AlertEventTypeDeadManSuccess,
		TaskID:    task.ID,
		Metadata:  map[string]string{"tracker": ReviewFixDeadManTrackerName},
		Timestamp: time.Now(),
	})
	return result, nil
}

// parseReviewFixed returns the items the session listed after its last
// REVIEW_FIXED: marker: the rest of that line, then following bullet lines,
// split on ";" and stripped of bullets. At most reviewFixMaxListed are kept.
func parseReviewFixed(output string) []string {
	idx := strings.LastIndex(output, "REVIEW_FIXED:")
	if idx < 0 {
		return nil
	}
	var items []string
	add := func(s string) {
		for _, part := range strings.Split(s, ";") {
			part = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(part), "-*• "))
			if part != "" && len(items) < reviewFixMaxListed {
				items = append(items, prePRField(part, 200))
			}
		}
	}
	lines := strings.Split(output[idx+len("REVIEW_FIXED:"):], "\n")
	add(lines[0])
	for _, line := range lines[1:] {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "-") && !strings.HasPrefix(t, "*") && !strings.HasPrefix(t, "•") {
			break
		}
		add(t)
	}
	return items
}

// recordReviewFixEvent writes the pre_pr_fix execution event for fix.
func (r *Runner) recordReviewFixEvent(task *Task, fix *ReviewFixOutcome) {
	b, err := json.Marshal(struct {
		Status     string `json:"status"`
		PreFixSHA  string `json:"pre_fix_sha,omitempty"`
		PostFixSHA string `json:"post_fix_sha,omitempty"`
		Fixed      int    `json:"fixed"`
		ElapsedMS  int64  `json:"elapsed_ms"`
	}{string(fix.Status), fix.PreFixSHA, fix.PostFixSHA, len(fix.Fixed), fix.Elapsed.Milliseconds()})
	if err != nil {
		return
	}
	r.recordExecutionEvent(task.LogExecutionID(), memory.StagePrePRFix, string(b))
}
