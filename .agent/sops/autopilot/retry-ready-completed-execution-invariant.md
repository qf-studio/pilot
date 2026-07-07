# SOP: pilot-retry-ready and HasCompletedExecution — the invariant that must hold

**Category:** Autopilot / GitHub SDK poller reliability
**Implemented:** 2026-07-07
**Source incident:** GH-3992 (auto-retry re-dispatched a shipped task, orphan-row
delete raced the waiter into a false `task_failed` alert) → fixed by GH-4021

---

## The invariant

`shouldRetryRetryReadyIssue` (poller.go) treats any `HasCompletedExecution`
row it finds while `pilot-retry-ready` is set as **stale** — left over from
the PR-closed-without-merge event that set the label — and either ignores it
(`hasMergedWork`'s DB fallback is skipped under this label) or deletes it
(`InvalidateCompletion`) before re-dispatching.

That assumption is only safe if this holds:

> **A completed-execution row can never be genuinely fresh while
> `pilot-retry-ready` is set.**

Two things make this true today:

1. `notifyExternalClose` (controller.go, GH-3818/D10) reclassifies the
   completed row to `status="failed"` the instant it detects a PR closed
   without merging — the same call that sets `pilot-retry-ready`. So by the
   time the poller sees the label, the row backing it is no longer
   `"completed"`.
2. `clearRetryLabels` (controller.go, GH-4021) removes `pilot-retry-*` labels
   the moment a PR **does** merge — so a fresh, genuinely-shipped completed
   row can never coexist with the label past the merge event.

**The gap GH-3992 exploited:** between a later retry's execution completing
(PR created, row written as `"completed"`) and that PR actually merging
(when #2 above fires), there's a window where the label is still set and the
row is genuinely fresh. `hasOpenPRAwaitingMerge` closes this window — an
open PR is unambiguous "don't re-dispatch" evidence regardless of label
staleness, checked *before* any invalidation happens.

## Do NOT add a raw, unconditional `HasCompletedExecution` check here

It's tempting — the normal dispatch path has exactly this check
(poller.go, "Skipping re-dispatch — completed execution exists"), and GH-4021's
issue text even cites those line numbers as the model to mirror. Adding it
verbatim to `shouldRetryRetryReadyIssue`, positioned before
`InvalidateCompletion` runs, breaks the **legitimate** retry-ready flow:

- `TestPoller_RetryReady_InvalidatesCompletedExecution` pins this — a
  completed row legitimately exists at that point (simulating a stale
  PR-closed-without-merge row) and must be invalidated + retried, not
  skipped.
- Reclassification depends on `Controller.evalStore` being non-nil.
  `TestNotifyExternalClose_ReclassifyNotCalledWithoutEvalStore` confirms nil
  `evalStore` is a supported configuration — in that config the stale row
  is *never* reclassified, so a raw check would permanently block
  retry-ready re-dispatch for any deployment without an eval store wired up.

If you're tempted to add this check: use `hasOpenPRAwaitingMerge` (timing-safe,
no false-positive risk) instead, or extend `clearRetryLabels`'s call sites,
not a bare `HasCompletedExecution` gate ahead of `InvalidateCompletion`.

## Where the three GH-4021 fixes live

- `poller.go` `shouldRetryRetryReadyIssue`: `hasOpenPRAwaitingMerge` guard
  (closes the open-PR window).
- `controller.go` `clearRetryLabels` + its two call sites (`handleMerging`,
  `checkExternalMergeOrClose`) and `poller.go` `hasMergedWork`: clear
  `pilot-retry-*` the moment work ships, so the label can't outlive the merge.
- `dispatcher.go` `WaitForExecution`: resolves a mid-wait `sql.ErrNoRows`
  (row deleted by `recoverStaleRunningTasks` orphan cleanup) against
  `HasCompletedExecution` instead of surfacing it as a waiter error.
