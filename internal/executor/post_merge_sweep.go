package executor

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// postMergeSweepGrace is added to the review timeout to get the age past which
// a review directory cannot belong to a live review.
const postMergeSweepGrace = time.Hour

// PostMergeSweepThreshold is the age past which a pilot-worktree-review-*
// directory is stale: the review timeout plus one hour.
func PostMergeSweepThreshold(reviewTimeout time.Duration) time.Duration {
	if reviewTimeout <= 0 {
		reviewTimeout = defaultPostMergeReviewTimeout
	}
	return reviewTimeout + postMergeSweepGrace
}

// SweepStalePostMergeCheckouts removes pilot-worktree-review-* directories
// under tmpDir last modified before now-olderThan, then runs git worktree
// prune in each project so the stale .git/worktrees entries a crashed review
// left go too (GH-5920). It returns the number of directories removed and of
// projects pruned, and logs both at Info. Symlinks and plain files that match
// the prefix are left alone.
func SweepStalePostMergeCheckouts(ctx context.Context, tmpDir string, olderThan time.Duration, now time.Time, projectPaths []string) (removed, pruned int) {
	log := slog.Default().With("component", "post-merge-sweep")
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		log.Warn("post-merge checkout sweep: cannot read temp directory", slog.String("dir", tmpDir), slog.Any("error", err))
		return 0, 0
	}
	cutoff := now.Add(-olderThan)
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), postMergeCheckoutPrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		path := filepath.Join(tmpDir, e.Name())
		if err := os.RemoveAll(path); err != nil {
			log.Warn("post-merge checkout sweep: cannot remove stale directory", slog.String("path", path), slog.Any("error", err))
			continue
		}
		removed++
	}

	for _, p := range projectPaths {
		if p == "" {
			continue
		}
		if _, err := gitInDir(ctx, p, "worktree", "prune"); err != nil {
			log.Warn("post-merge checkout sweep: git worktree prune failed", slog.String("project", p), slog.Any("error", err))
			continue
		}
		pruned++
	}
	log.Info("post-merge checkout sweep",
		slog.Int("removed", removed), slog.Int("projects_pruned", pruned),
		slog.Duration("older_than", olderThan))
	return removed, pruned
}
