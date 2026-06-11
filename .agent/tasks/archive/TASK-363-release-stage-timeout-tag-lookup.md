# TASK-363: Release stage hardening — retry timeout + unbounded tag lookup

**Status**: ✅ **SHIPPED in v2.186.1** (2026-06-10 18:53Z) — PR [#3559](https://github.com/qf-studio/pilot/pull/3559) reviewed+approved, **hand-merged** (`09dcb16e`) after the autopilot terminally failed it: the **size-floor gate** (`scope_guard.go:63`, >200 net added lines; PR adds 656) escalates to approval by design, but `approval.pre_merge.enabled=false` in config → fail-closed → `StageFailed` ("approval-misconfig" — error text misleadingly blames `require_approval=true`; stage actually has `require_approval: false`). Daemon then cut the v2.186.1 tag ITSELF via the merged-PR scan, at the correct reachable commit. Artifact-verified at main HEAD per mem-033: `maxPages = 50` pagination in client.go, `guardReleaseSHAReachable` + `checkReleasingRetryOrEscalate` in controller.go. Diff-verified against spec: retry cap (`MaxReleasingAttempts`, default 10, escalates to `StageFailed` + issue comment), exhaustive-tag drain branch, `ListTags(...,20)` GONE from `GetTagForSHA` (paginates 100×50), **TASK-362 reachability guard present**. 9 new tests. Minor: `ReleasingFirstAt` recorded but unread (timeout half realized as attempt cap — acceptable). ⚠️ Code takes effect when the daemon upgrades to ≥v2.186.1 (daemon on v2.186.0 at ship time). **Side-discovery (policy decision for user)**: with pre_merge approvals disabled, EVERY oversized pilot PR (>200 net lines) dead-ends at `StageFailed` — enable `approval.pre_merge.enabled: true` (Telegram approvals wired, approver `283716179`) or keep the hand-merge flow.
Original handoff: GH issue [#3557](https://github.com/qf-studio/pilot/issues/3557) (2026-06-10, Wave 2; **expanded to also carry the TASK-362 tag-reachability guard** dropped when #3541 was falsely superseded — user-approved fold, priority raised to P1). Gate was satisfied: 362's executor half merged via PR #3548 (v2.185.1); nothing else in flight touches `internal/autopilot/` or the github client.
⚠️ The daemon **ignored the "must not be decomposed" instruction** and decomposed #3557 anyway — into a single child [#3558](https://github.com/qf-studio/pilot/issues/3558) (`inherited-spec: true`) carrying the full three-defect spec.
**2026-06-10 ~17:40: parent #3557 closed manually** — its orchestrator was burning laps re-executing #3558 with EMPTY prompts after each daemon restart (recovered-child bug, TASK-364 Hole 5). #3558 reset to bare `pilot` label → poller dispatches it standalone with the real body (the path that worked for #3540/#3541). **#3558 is now the sole carrier of this task** — verify the artifact on its PR.
**Priority**: P2 (hazard; self-cleared last occurrence but unfixed)
**Origin**: 14h20m stuck-release diagnosis, 2026-06-09→10 (see TASK-361 context)

## Problem

Two compounding defects in the autopilot release pipeline:

1. **`handleReleasing` has no timeout/escape** (`internal/autopilot/controller.go` ~:1860, post-#3527 line numbers will differ — grep). If any GitHub call inside it errors, the dispatcher records the failure and the PR **stays in `StageReleasing`, retried every poll cycle, forever**. Observed: a PR spun at "◒ release" for 14h20m. The only backstop is the GH-849 deadlock *alert* after 1h — it alerts but never drains. On daemon restart, `RestoreState` (~:398) re-adopts any non-`StageFailed` row, so the loop resumes every `pilot start`.

2. **`GetTagForSHA` scans only the 20 most-recent tags** (`internal/adapters/github/client.go` ~:655-656, `ListTags(..., 20)`). The "commit already tagged → short-circuit as success" check misses older tags, so an already-released commit can be re-tagged/re-released or loop erroring.

Display note: the TUI "0/3" next to the spinner is the **failure counter** (`failures/maxFailures`, default 3) — not release progress. Renderer at `internal/dashboard/tui.go` ~:192-208.

## Fix direction

- `handleReleasing`: treat an existing **published release** for `HeadSHA` as success → drain (`removePR`), not retry. Add a hard cap (time- or attempt-based) on the releasing stage; on cap, transition to `StageFailed` (which `RestoreState` already skips) + alert.
- `GetTagForSHA`: for the short-circuit check, resolve the tag for a SHA without the 20-tag window (GraphQL or paginate; a direct `compare`/`refs` lookup beats listing).
- Keep the existing TASK-316 duplicate-tag-as-success guards intact.

## Acceptance criteria

- [ ] A PR whose commit already has a published release drains as success on the next cycle
- [ ] Releasing stage cannot retry indefinitely — bounded, then `StageFailed` + alert
- [ ] Tag-for-SHA lookup not bounded to 20 newest tags
- [ ] Unit tests for: already-released drain, retry cap → StageFailed, old-tag lookup

## Cross-refs

- TASK-361 · `.agent/knowledge/memories/patterns/pattern_burst_auto_release_starvation.md` · `pattern_autopilot_pr_state_ephemeral.md`
- TASK-316 (existing tag guards)
