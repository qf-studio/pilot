package executor

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"
)

// reviewHygieneTimeout bounds the git calls reviewPhaseTreeHygiene makes.
// They run on a fresh context because the review context is usually the thing
// that just expired.
const reviewHygieneTimeout = 30 * time.Second

// reviewPhaseOutcome records how a review phase (the pre-PR fix round) ended so
// the caller can decide what to do with edits the session left in the working
// tree. GH-5900.
type reviewPhaseOutcome struct {
	ran     bool          // the backend was actually invoked (not skipped)
	fixed   bool          // output carried REVIEW_FIXED:
	failed  bool          // the backend returned an error
	killed  bool          // the session was terminated (timeout / cancellation)
	elapsed time.Duration // wall-clock time until the failure, set when failed
}

// reviewPhase names a review phase for tree hygiene: the label in its log lines
// and the message of the commit that keeps its edits.
type reviewPhase struct {
	name          string
	commitMessage string
}

var reviewFixPhase = reviewPhase{name: "Review-fix", commitMessage: "chore(review-fix): apply pre-PR review fixes"}

// hygieneAction is what a hygiene pass did to the tree.
type hygieneAction string

const (
	hygieneNone      hygieneAction = ""          // nothing to do or git unreadable
	hygieneCommitted hygieneAction = "committed" // edits kept in a new commit
	hygieneDiscarded hygieneAction = "discarded" // edits reset away (or reset failed)
)

// reviewWasTerminated reports whether err means the review session was
// killed by its context (deadline or cancellation) rather than failing on
// its own: the shutdown_terminated / timeout backend error types, or a dead
// review context.
func reviewWasTerminated(reviewCtx context.Context, err error) bool {
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

// reviewPhaseTreeHygiene leaves the working tree clean after a review phase,
// whatever way it ended. The prompt tells the session to fix what it finds, so
// it edits files in the tree; nothing else commits or resets those edits, and a
// dirty clone fails the git_clean pre-flight of every later task on the project.
// The pre-PR fix round runs it in both modes, because the worktree is what
// gets pushed.
//
// Dirty tree + REVIEW_FIXED + session not killed -> commit the edits on the
// task branch. Anything else -> reset to HEAD. Logs one Info line naming the
// action, file count and reason (committed / discarded-killed /
// discarded-failed / discarded-unreported).
func (r *Runner) reviewPhaseTreeHygiene(task *Task, git *GitOperations, phase reviewPhase, out reviewPhaseOutcome) hygieneAction {
	if !out.ran || git == nil {
		return hygieneNone
	}
	log := r.log.With(slog.String("task_id", task.ID))

	ctx, cancel := context.WithTimeout(context.Background(), reviewHygieneTimeout)
	defer cancel()

	dirty, err := git.dirtyProjectPaths(ctx)
	if err != nil {
		log.Warn(phase.name+" tree hygiene: could not read git status", slog.Any("error", err))
		return hygieneNone
	}
	if len(dirty) == 0 {
		return hygieneNone
	}

	if out.fixed && !out.killed && !out.failed {
		_, commitErr := git.Commit(ctx, phase.commitMessage)
		if commitErr == nil {
			log.Info(phase.name+" tree hygiene: committed edits",
				slog.String("action", "committed"),
				slog.Int("files", len(dirty)),
				slog.String("reason", "committed"),
			)
			return hygieneCommitted
		}
		log.Warn(phase.name+" tree hygiene: commit failed, discarding edits", slog.Any("error", commitErr))
		discardReviewEdits(ctx, log, git, phase, len(dirty), "discarded-failed")
		return hygieneDiscarded
	}

	reason := "discarded-unreported"
	switch {
	case out.killed:
		reason = "discarded-killed"
	case out.failed:
		reason = "discarded-failed"
	}
	discardReviewEdits(ctx, log, git, phase, len(dirty), reason)
	return hygieneDiscarded
}

func discardReviewEdits(ctx context.Context, log *slog.Logger, git *GitOperations, phase reviewPhase, files int, reason string) {
	if err := git.ResetHardToCommit(ctx, "HEAD"); err != nil {
		log.Warn(phase.name+" tree hygiene: reset failed; tree may be dirty",
			slog.Int("files", files), slog.String("reason", reason), slog.Any("error", err))
		return
	}
	log.Info(phase.name+" tree hygiene: discarded edits",
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
