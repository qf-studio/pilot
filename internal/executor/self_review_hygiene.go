package executor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// selfReviewHygieneTimeout bounds the git calls selfReviewTreeHygiene makes.
// They run on a fresh context because the review context is usually the thing
// that just expired.
const selfReviewHygieneTimeout = 30 * time.Second

// selfReviewOutcome records how the self-review phase ended so the caller can
// decide what to do with edits the session left in the working tree. GH-5900.
type selfReviewOutcome struct {
	ran     bool          // the backend was actually invoked (not skipped)
	fixed   bool          // output carried REVIEW_FIXED:
	failed  bool          // the backend returned an error
	killed  bool          // the session was terminated (timeout / cancellation)
	elapsed time.Duration // wall-clock time until the failure, set when failed
}

// selfReviewWasTerminated reports whether err means the review session was
// killed by its context (deadline or cancellation) rather than failing on
// its own: the shutdown_terminated / timeout backend error types, or a dead
// review context.
func selfReviewWasTerminated(reviewCtx context.Context, err error) bool {
	if reviewCtx != nil && reviewCtx.Err() != nil {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var be BackendError
	if errors.As(err, &be) {
		switch be.ErrorType() {
		case string(ErrorTypeShutdownTerminated), string(ErrorTypeTimeout):
			return true
		}
	}
	return false
}

// selfReviewPRNote is the PR-body line for a terminated self-review; empty
// when the review was not terminated.
func selfReviewPRNote(terminatedAfter time.Duration) string {
	if terminatedAfter <= 0 {
		return ""
	}
	return fmt.Sprintf("\n\nSelf-review was terminated after %ds; its edits were discarded.", int(terminatedAfter.Seconds()))
}

// afterSelfReview runs once the self-review phase has returned (success, error
// or timeout). In direct mode it cleans the clone via selfReviewTreeHygiene;
// worktree mode skips that, since the worktree is discarded. Returns how long
// the session ran when it was terminated, else zero.
func (r *Runner) afterSelfReview(task *Task, git *GitOperations, state *progressState, executed, useWorktree bool) time.Duration {
	if !executed {
		return 0
	}
	if !useWorktree {
		r.selfReviewTreeHygiene(task, git, state.selfReview)
	}
	if state.selfReview.killed {
		return state.selfReview.elapsed
	}
	return 0
}

// selfReviewTreeHygiene leaves the direct-mode project clone clean after the
// self-review phase, whatever way it ended. The prompt tells the session to
// fix what it finds, so it edits files in the clone; nothing else commits or
// resets those edits, and a dirty clone fails the git_clean pre-flight of
// every later task on the project. Callers skip it in worktree mode, where the
// worktree is discarded.
//
// Dirty tree + REVIEW_FIXED + session not killed -> commit the edits on the
// task branch. Anything else -> reset to HEAD. Logs one Info line naming the
// action, file count and reason (committed / discarded-killed /
// discarded-failed / discarded-unreported).
func (r *Runner) selfReviewTreeHygiene(task *Task, git *GitOperations, out selfReviewOutcome) {
	if !out.ran || git == nil {
		return
	}
	log := r.log.With(slog.String("task_id", task.ID))

	ctx, cancel := context.WithTimeout(context.Background(), selfReviewHygieneTimeout)
	defer cancel()

	dirty, err := git.dirtyProjectPaths(ctx)
	if err != nil {
		log.Warn("Self-review tree hygiene: could not read git status", slog.Any("error", err))
		return
	}
	if len(dirty) == 0 {
		return
	}

	if out.fixed && !out.killed && !out.failed {
		_, commitErr := git.Commit(ctx, "chore(self-review): apply self-review fixes")
		if commitErr == nil {
			log.Info("Self-review tree hygiene: committed self-review edits",
				slog.String("action", "committed"),
				slog.Int("files", len(dirty)),
				slog.String("reason", "committed"),
			)
			return
		}
		log.Warn("Self-review tree hygiene: commit failed, discarding edits", slog.Any("error", commitErr))
		discardSelfReviewEdits(ctx, log, git, len(dirty), "discarded-failed")
		return
	}

	reason := "discarded-unreported"
	switch {
	case out.killed:
		reason = "discarded-killed"
	case out.failed:
		reason = "discarded-failed"
	}
	discardSelfReviewEdits(ctx, log, git, len(dirty), reason)
}

func discardSelfReviewEdits(ctx context.Context, log *slog.Logger, git *GitOperations, files int, reason string) {
	if err := git.ResetHardToCommit(ctx, "HEAD"); err != nil {
		log.Warn("Self-review tree hygiene: reset failed; clone may be dirty",
			slog.Int("files", files), slog.String("reason", reason), slog.Any("error", err))
		return
	}
	log.Info("Self-review tree hygiene: discarded self-review edits",
		slog.String("action", "discarded"),
		slog.Int("files", files),
		slog.String("reason", reason),
	)
}

// dirtyProjectPaths lists dirty paths (staged, unstaged, untracked) that are
// not part of the excluded Navigator/scaffold set.
func (g *GitOperations) dirtyProjectPaths(ctx context.Context) ([]string, error) {
	out, err := g.gitOutput(ctx, "status", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, entry := range strings.Split(strings.TrimRight(out, "\x00"), "\x00") {
		// "XY path"; the bare origin-path entry that follows a rename has no XY prefix.
		if len(entry) < 4 || entry[2] != ' ' {
			continue
		}
		if p := entry[3:]; !isExcluded(p) {
			paths = append(paths, p)
		}
	}
	return paths, nil
}
