package autopilot

import (
	"context"
	"math/rand"
	"sort"
	"time"
)

// StaggerRepoScans runs scanFn once per repo in repos, serialized with a
// jittered delay between each invocation (GH-4391) instead of firing every
// repo's startup scan back-to-back at boot. The incident that motivated this
// package was 11 repos' worth of startup scans (ScanExistingPRs +
// ScanRecentlyMergedPRsAtStartup, each a wide catch-up sweep) bursting
// simultaneously and exhausting the shared GitHub rate budget within the
// first minute, 403'ing every issue poller for 67+ minutes.
//
// The first repo (in sorted-name order, for deterministic behavior) runs
// immediately; every subsequent repo waits interval +/- 25% jitter before
// its scanFn call. interval <= 0 disables staggering entirely (all repos run
// back-to-back), matching the pre-GH-4391 behavior — useful for tests and
// single-repo deployments where staggering has no effect anyway.
//
// Returns early, skipping any remaining repos, if ctx is cancelled while
// waiting between repos.
func StaggerRepoScans(ctx context.Context, repos map[string]*Controller, interval time.Duration, scanFn func(ctx context.Context, repoName string, c *Controller)) {
	if len(repos) == 0 {
		return
	}
	names := make([]string, 0, len(repos))
	for name := range repos {
		names = append(names, name)
	}
	sort.Strings(names)

	for i, name := range names {
		if i > 0 && interval > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(jitteredStaggerDelay(interval)):
			}
		}
		if ctx.Err() != nil {
			return
		}
		scanFn(ctx, name, repos[name])
	}
}

// jitteredStaggerDelay returns interval +/- 25% jitter. Jitter (rather than a
// fixed interval) avoids every daemon restart re-synchronizing its per-repo
// scan schedule to the same wall-clock offsets across process lifetimes.
func jitteredStaggerDelay(interval time.Duration) time.Duration {
	if interval <= 0 {
		return 0
	}
	spread := int64(interval) / 2 // +/- 25% of interval = a 50%-of-interval-wide spread
	if spread <= 0 {
		return interval
	}
	jitter := time.Duration(rand.Int63n(spread)) - time.Duration(spread/2)
	d := interval + jitter
	if d < 0 {
		return 0
	}
	return d
}
