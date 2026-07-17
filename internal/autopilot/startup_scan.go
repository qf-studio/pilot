package autopilot

import (
	"context"
	"math/rand"
	"time"
)

// StartupScanStaggerBase and StartupScanStaggerJitter bound the delay
// RunStaggeredStartupScans waits between each repo's startup-scan slot.
// GH-4391: bursting every configured repo's startup rescan in the same
// instant is what produced 173x secondary-rate-limit 503s on the founder
// box (2026-07-16 22:26Z) — GitHub's abuse-detection layer penalizes
// concurrent bursts independently of the primary rate limit, so serializing
// with jitter addresses that even though the requests themselves are cheap.
const (
	StartupScanStaggerBase   = 2 * time.Second
	StartupScanStaggerJitter = 2 * time.Second
)

// RunStaggeredStartupScans performs the two startup catch-up scans
// (ScanExistingPRs, ScanRecentlyMergedPRsWithWindow) plus Start's recovery
// sweeps, one repo at a time with a jittered delay between slots — instead
// of bursting every controller's startup scan concurrently (GH-4391).
//
// ScanExistingPRs is never gated by the rate-limit budget floor: restoring
// in-flight PR state on restart is the same priority tier as the pollers.
// ScanRecentlyMergedPRsWithWindow honors backgroundScanAllowed and the
// conditional-probe skip internally, so a rate-starved or quiet repo costs
// little to nothing here.
//
// sleepFn is injected so tests can run this instantly (e.g. a no-op or a
// call counter); production callers pass time.Sleep. A nil sleepFn defaults
// to time.Sleep. Map iteration order (and therefore scan order) is
// intentionally left up to Go's randomized map ordering — no repo is
// special-cased.
func RunStaggeredStartupScans(ctx context.Context, controllers map[string]*Controller, scanWindow time.Duration, sleepFn func(time.Duration)) {
	if sleepFn == nil {
		sleepFn = time.Sleep
	}

	first := true
	for repoName, controller := range controllers {
		if ctx.Err() != nil {
			return
		}
		if !first {
			delay := StartupScanStaggerBase
			if StartupScanStaggerJitter > 0 {
				delay += time.Duration(rand.Int63n(int64(StartupScanStaggerJitter)))
			}
			sleepFn(delay)
		}
		first = false

		if err := controller.ScanExistingPRs(ctx); err != nil {
			controller.log.Warn("startup: failed to scan existing PRs", "repo", repoName, "error", err)
		}
		if err := controller.ScanRecentlyMergedPRsWithWindow(ctx, scanWindow); err != nil {
			controller.log.Warn("startup: failed to scan merged PRs", "repo", repoName, "error", err)
		}
		controller.Start(ctx)
	}
}
