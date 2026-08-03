---
name: poller-labels-removed-log-means-never-applied
description: Poller log "Issue was processed but status labels removed, allowing retry" fires when pilot-in-progress/pilot-done are ABSENT — it cannot distinguish "removed" from "never applied"; paired with hasCompletedExecution it becomes a benign ~5-min self-resetting log loop that looks like an active incident
type: pitfall
---

# "status labels removed" log fires on never-applied labels too — benign 5-min loop reads as incident

**What happened (2026-08-03, aws-infrastructure-pilot#2):** after onboarding
the repo, the daemon logged every ~5 minutes:

```
Issue was processed but status labels removed, allowing retry
Skipping re-dispatch — completed execution exists
```

Investigation (issue timeline via `gh api .../issues/2/timeline --paginate`):
**zero `unlabeled` events ever existed.** `pilot-in-progress` was never
applied to the issue — the label doesn't exist anywhere in that repo's label
set (while `pilot-done` was auto-created and applied fine at close). The log
wording implies an external actor removed labels; the actual condition is
"expected status labels absent", which includes never-applied.

**The loop mechanics** (studio-sdk `sdk/integrations/github/poller.go:790,
:1043, :1543-1563`): grace period (5 min) elapses → issue unmarked →
`hasCompletedExecution()` (DB-backed, doc comment: *"prevents re-dispatch
when the done label failed to apply"*) blocks dispatch → re-marks processed
→ grace period resets → repeat forever. No duplicate dispatch — the
`execution_claims` admission table (TASK-407) sits underneath as a second
gate. Loop ends when the issue closes.

**How to apply:**
- Seeing this line ≠ something is stripping labels. First check the issue
  timeline for actual `unlabeled` events and the repo's label list for
  whether `pilot-in-progress` even exists there.
- GH Projects/board automation was ruled out for this incident — red
  herring; check timelines before suspecting workflows.
- Root cause (traced 2026-08-03, filed GH-4687): since the 07-16 SDK-poller
  cutover **no live path applies `pilot-in-progress` on any repo** — the only
  producer is the webhook-only legacy handler (`internal/pilot/pilot.go:1192`);
  the SDK dispatch chain (`cmd/pilot/handlers.go:693` → `handler_common.go`)
  does zero label ops, and studio-sdk's `Notifier` is shipped but unwired.
  Repos with the label in their set carry a historical artifact. Downstream,
  `recoverOrphanedIssues` and the pre-dispatch label guards are structurally
  dead, and `controller.go:3014`'s post-merge `RemoveLabel` is a swallowed
  no-op. Same silently-dead-since-cutover class as GH-4669.
- Log-quality candidate (studio-sdk `poller.go:790`/`:1043`, separate release
  cycle — follow-up only): reword to "expected status labels absent (removed
  or never applied)" and demote repeats per issue to DEBUG.

Related: [[config-env-expansion-eats-dollar-vars-in-commands]] (same
onboarding session; new-repo first-run rough edges).
