# Pitfall: autopilot_deadlock re-fired hourly while idle because mark-sent and fire conditions differed

## Summary
autopilot_deadlock fired every hour on an idle box (#5448): MetricsAlerter.evaluate marked the alert sent only when len(activePRs)>0, while the engine's AlertTypeDeadlock case fires on no_progress_minutes alone, so idle never set the flag and the 1h cooldown re-fired forever. Fixed in PR#5465 (idle → no_progress 0 + Controller.ResetProgressClock, since registerPR/RestoreState/merged-PR scan never touch lastProgressAt). Class: emitter-side dedupe flag guarded by a condition the engine does not share; keep fire and mark-sent conditions identical.

## Context
#5448 (founder box, 17 alerts overnight 2026-09-21). Fixed by external PR#5465, merged 2026-09-30 (540947e3f), ships in v2.276.8. Verified by two mutation pins during review.

## Details
autopilot_deadlock fired every hour on an idle box (#5448): MetricsAlerter.evaluate marked the alert sent only when len(activePRs)>0, while the engine's AlertTypeDeadlock case fires on no_progress_minutes alone, so idle never set the flag and the 1h cooldown re-fired forever. Fixed in PR#5465 (idle → no_progress 0 + Controller.ResetProgressClock, since registerPR/RestoreState/merged-PR scan never touch lastProgressAt). Class: emitter-side dedupe flag guarded by a condition the engine does not share; keep fire and mark-sent conditions identical.

## Recommended Approach
Keep the emitter-side dedupe flag and the engine-side fire condition identical, or move the whole decision to one side. Anything that adds PRs to `activePRs` (`registerPR`, `RestoreState`, merged-PR scan, `scope_release.go`) does not touch `lastProgressAt`; only stage transitions in `ProcessPR` do. Follow-up test pins: #5499.

## Related
- [[alert-cooldown-per-rule-not-per-source]]
- [[alert-engine-counts-operator-cancels]]
- #5448, PR#5465, #5499
- `internal/autopilot/metrics_alerter.go`, `internal/autopilot/controller.go`

---
**Captured**: 2026-09-30
**Confidence**: 90%
**Concepts**: alerts, autopilot
