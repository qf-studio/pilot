---
name: poller-labels-in-progress-before-dispatcher-claim-wedge
description: The SDK poller applies pilot-in-progress (notifyTaskStartedSDK) BEFORE the dispatcher accepts the claim; when the dispatcher drops the pickup (repick backoff → "dispatch claim lost", returning "",nil) nothing unwinds the label — no execution runs, but the label makes every later poll tick skip the issue. Permanent wedge; recovery = manually remove pilot-in-progress. Fix tracked as pilot#4961.
type: pitfall
created: 2026-08-18
---

# Poller labels `pilot-in-progress` before the dispatcher claim — a dropped claim wedges the issue

**What happened (2026-08-18, S3 exit pass).** Two of three hosted tenant boxes
(v2.259.3) wedged identically after a declined execution:

1. Execution declines → lifecycle strips `pilot-in-progress`.
2. Next poll tick (~30s later): poller dispatches the still-open issue →
   `notifyTaskStartedSDK` re-applies `pilot-in-progress`.
3. Dispatcher drops the pickup: `dispatch re-pick throttled — task still within
   repick backoff window` → `dispatch claim lost … dropping duplicate pickup`
   (both return `"", nil` in `internal/executor/dispatcher.go`).
4. Wedged: label present, no execution running, poller skips the labeled issue
   forever. Stale-label cleanup did not fire within 10+ minutes.

**Code shape (confirmed unchanged on main, 2026-08-18):**
`cmd/pilot/handlers.go handleGithubIssueEventSDK` calls `notifyTaskStartedSDK`
(GH-4687: label at start of work) before `handleIssueGeneric` reaches the
dispatcher; the dispatcher's drop paths return empty execID with nil error and
no callback unwinds the label.

**Recovery:** `gh issue edit N --remove-label pilot-in-progress` — the poller
re-dispatches on the next tick (repick backoff window will have passed) and the
run completes normally (both wedged tenants shipped their PR immediately after
the strip).

**How this compounds with the decline class:** declines make the trigger common
(decline → unmark → instant re-dispatch lands inside the backoff window), so
[[pilot-issue-missing-no-decompose-fragments-single-fix]] / TASK-480 territory
(duplicate-spec issues, safe no-op contract) is where this fires most.

**Fix tracked:** pilot#4961 — unwind the label when the same dispatch attempt's
claim is dropped, or apply the label only after `lifecycle.Begin` succeeds
(mind the GH-4687 happy-path expectations and the label-lifecycle dead-man
tracker semantics).
