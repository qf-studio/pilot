# SOP: A PR that cannot persist to the state store must escalate, not spin

**Category:** Autopilot / SQLite state store
**Implemented:** 2026-07-08
**Source incident:** GH-4053 — reconciler-adopted PR #4047 looped every poll tick on `"SQL logic error: ON CONFLICT clause does not match any PRIMARY KEY or UNIQUE constraint (1)"` from `SavePRState`. 22+ occurrences over hours; the PR could never advance stage (every stage handler ends in `persistPRState`), and nothing outside the log stream ever signaled it.

## Problem

`persistPRState` (controller.go) only logged `WARN` on a `SavePRState` failure and returned. Any persist failure that isn't transient (schema drift, a corrupted/partially-migrated row, a residual single-column `ON CONFLICT` clause vs. the live composite `PRIMARY KEY (repo, pr_number)`) wedges the PR forever: the row can never be saved, so it can never transition stage, so it retries the exact same failing write every `processAllPRs` tick with no operator-visible signal beyond a WARN line.

## Fix

`persistPRState` now tracks `PRState.PersistFailureCount` (in-memory only, reset to 0 on a successful persist):
- On the **first** failure for a PR, `alertPersistFailureOnce` fires a `pr_persist_failed` alert (deduplicated per PR number via `alertedPersistFailures`, mirroring the `alertedMissingReleases` pattern from GH-3927/GH-3991) — so the failure is visible outside the log stream immediately, not after a human happens to notice log spam.
- After `persistFailureEvictThreshold` (5, mirroring the GH-3903 404-eviction threshold) consecutive failures, `evictPersistFailedPR` drops the PR from `activePRs`/`prFailures`/`recordedMerges` and calls `persistRemovePR` — a plain `DELETE` with no `ON CONFLICT` clause, so it succeeds even when the upsert path that got the row into this state cannot. This is the same one-time row reconciliation a human would otherwise run by hand.
- The evicted PR number is recorded in `persistFailedPRs` (with a timestamp), and `reconcileOrphanPRs` / the startup PR scan both consult `recentlyEvictedForPersistFailure` before re-adopting an untracked open PR — otherwise the 60s reconciler sweep re-adopts the still-open PR on the very next tick and repeats the identical adopt-fail-evict cycle forever. The cooldown (1h) is intentionally not permanent: it's in-memory only and a daemon restart clears it, giving a fixed underlying issue (e.g. a corrected schema) a path back to normal tracking.

## Prevention

Any new per-PR write path added to `state_store.go` should go through `persistPRState`/`persistRemovePR` rather than a bespoke `db.Exec` call, so it inherits this escalation behavior automatically. If you add a *new* `ON CONFLICT` clause anywhere in `state_store.go`, verify its column list against the target table's actual `PRAGMA table_info` PK/unique-index columns — see `state-store-column-rebuild-drop.md` for why the live schema can drift from what a fresh `:memory:` DB would suggest.
