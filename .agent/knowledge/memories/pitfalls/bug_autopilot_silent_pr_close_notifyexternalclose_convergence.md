---
name: autopilot's direct PR-close call sites (handleCIFailed, handleReviewRequested, handleMergeConflict) never post comments themselves — the audit trail is only written a poll cycle later, in notifyExternalClose, which the pilot-done skip guard used to bypass entirely
description: Any code closing a PR in controller.go must set prState.Error (and prState.TerminalLabel if the issue must not be auto-retried) instead of trying to comment inline — notifyExternalClose is the single convergence point for every non-merge close and is where GH-3806's audit trail lives.
type: pitfall
---
**Symptom:** PR #3802 (fix for #3789) went CI-red and autopilot closed it and deleted
the branch with zero explanation — no PR comment naming the failing check, no issue
comment, and the source issue #3789 kept its `pilot-done` label from an earlier,
unrelated successful PR, making discarded work look shipped (TASK-382 D9, GH-3806).

**Root cause — two compounding gaps:**
1. `handleCIFailed`'s three direct `ClosePullRequest` call sites (iteration-limit,
   size-guard, and the main cascade path) never posted a PR comment and never
   touched the linked issue's labels at all. Same for `handleReviewRequested`'s two
   close sites.
2. The actual "why" only gets a chance to surface a poll cycle *later*: every close
   (autopilot's own, or a human's) becomes visible via `checkExternalMergeOrClose` →
   `notifyExternalClose`, which is the single place that historically added
   `pilot-retry-ready` / removed `pilot-in-progress`. But its `pilot-done` skip-guard
   (added for GH-2340, for the legitimate case of a duplicate PR closed after the
   *real* PR already merged) returned immediately with **zero comments posted at
   all** — silently eating the story exactly when an issue already had `pilot-done`
   from an earlier, different PR.

**Fix (GH-3806):** `notifyExternalClose` now always posts a PR comment (`AddPRComment`,
which — note — hits `/repos/.../issues/{prNumber}/comments`, not `/pulls/.../comments`)
and an issue comment built from `prState.Error`, on *every* branch including the
`pilot-done` and human-recovery-PR skip guards. Every direct close site in
`handleCIFailed`/`handleReviewRequested` now sets `prState.Error` to a specific,
human-readable reason (embedding failing check names / the follow-up issue number)
before returning — they don't post comments themselves, because `notifyExternalClose`
is the one place with confirmed proof the close is visible on GitHub. A new
`PRState.TerminalLabel` field (in-memory only, same pattern as `EscalationReason`)
lets a close site force `pilot-failed` instead of the default `pilot-retry-ready`
when the failure is terminal (iteration/size-guard cap) or a dependent follow-up
issue already owns the retry — otherwise `notifyExternalClose` would happily
re-queue a cascade that already hit its cap, or double-dispatch alongside the new
issue.

**How to apply:** any *new* code path that closes a PR in `controller.go` should set
`prState.Error` (always) and `prState.TerminalLabel = github.LabelFailed` (when the
issue must not be auto-retried under its own number) and then just return — do not
add ad-hoc `AddPRComment`/`AddComment` calls at the close site itself, and do not
add new logic to the `pilot-done`/human-recovery skip-guard branches without also
keeping the comment posted. Relates to [[bug_daemon_autoupgrade_reverts_dev_binary]]
(same TASK-382 defect-burndown epic).
