package autopilot

import (
	"fmt"
	"time"
)

// releasePlanMessage renders the release-aware next-step text shown on the
// Telegram approval ack card once a human approves a merge (GH-4164). It is
// injected onto approval.Request.ReleasePlan by submitAsyncApprovalRequest at
// request-creation time — the approval package itself never imports release
// config, so this function is the only place that translates a
// ReleaseConfig into approval-facing text.
func releasePlanMessage(rel *ReleaseConfig, now time.Time) string {
	if rel == nil || !rel.Enabled {
		return "No releaser configured for this repo (merge only)."
	}

	switch rel.Trigger {
	case "on_schedule":
		nextRun, err := nextScheduledRunString(rel, now)
		if err != nil {
			return "Rides the next release train (schedule unavailable — check release config)."
		}
		return fmt.Sprintf("Rides the next release train: %s.", nextRun)
	case "on_merge", "":
		return "Will release immediately after merge."
	default:
		// on_scope_close / manual: not one of the three cases GH-4164 enumerates
		// explicitly. Both hold merges rather than releasing immediately after
		// this one, so the generic "merge only" wording is accurate enough
		// without inventing a fourth ack-card variant.
		return "No releaser configured for this repo (merge only)."
	}
}
