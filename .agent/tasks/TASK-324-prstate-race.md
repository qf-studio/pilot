# TASK-324: Guard `*PRState` field access — cross-goroutine data race in the autopilot controller

**Wave:** 0 (M) · **MANUAL — do NOT hand to Pilot** (concurrency correctness in the live merge
state-machine) · **Severity:** CRITICAL (consolidates 1 critical + 1 high audit finding — same hazard) ·
**Audit ref:** TASK-322 §critical #3 + §high "Data race on shared *PRState" (`concurrency` / `autopilot`) ·
**Status:** DESIGNED (surface mapped below); implementation pending a focused pass.

---

## Problem

`Controller` stores `*PRState` in `activePRs` and hands the **live pointers** out, then mutates struct
fields with `c.mu` guarding only the **map**, never the pointed-to struct. Field access happens from
multiple goroutines with no common lock → genuine data race (trips `-race`) + correctness hazard
(a webhook can flip a mid-`StageMerging` PR to `StageReviewRequested`; torn `Stage` reads drive the
wrong branch).

## Surface map (discovered during design — this is why it's an M, not a lock sprinkle)

**Writers of `*PRState` fields:**
- main loop: `ProcessPR` (467) → 11 `handleX(prState)` methods (all mutate); inline `PRTitle`/`TargetBranch`/`Error`.
- main loop: `processAllPRs` (2505) — `checkExternalMergeOrClose` (mutates), changes-requested `pr.Stage=`.
- webhook goroutines: `OnReviewRequested` (420) `pr.Stage=`, `OnPRCreated` (378) insert+persist,
  `SetApprovalDecision` (1168) → `applyApprovalDecision` (1141) sets `ApprovalDecision`.

**Readers of `*PRState` fields (each a distinct goroutine, all racing the writers):**
- `processAllPRs` debug logs + `metrics.UpdateActivePRs` (reads `pr.Stage`).
- `metrics_alerter.go:138` `evaluate()` — separate ticker goroutine, reads `Stage`/`CIWaitStartedAt`.
- `dashboard/tui.go:161` — TUI goroutine.
- `gateway/server.go:511` + `cmd/pilot/adapters.go:282` — HTTP goroutine.
All six obtain pointers via `GetActivePRs()` (1929), which currently returns **live** pointers.

## Design (locked)

Per-`PRState` mutex + **`GetActivePRs` returns detached snapshots** so every read-only consumer is safe
for free; only the two internal mutators use live pointers under the lock.

1. **`types.go`** — add `mu sync.Mutex` to `PRState` (embedded). NOTE: never value-copy a populated
   `PRState` after this (`go vet` copylocks). `state_store.go` uses `var pr PRState` (zero value) — fine.
2. **`GetActivePRs()`** — return `[]*PRState` of **copies** built under each `pr.mu` via an explicit
   field-by-field `snapshot()` helper (NOT `*pr` — copylocks). Fixes metrics, metrics_alerter, dashboard,
   gateway readers with zero per-consumer edits.
3. **`ProcessPR`** — after fetching the live pointer from the map (under `c.mu.RLock`), take
   `prState.mu.Lock(); defer prState.mu.Unlock()` for the whole body. One lock covers all 11 handlers +
   inline writes + the `persistPRState` call.
4. **`processAllPRs`** — use the `GetActivePRs()` snapshots for `metrics.UpdateActivePRs` + the iteration
   list; for each, re-fetch the **live** pointer by number, take `pr.mu` around `checkExternalMergeOrClose`
   + the changes-requested RMW + persist, release, then call `ProcessPR` (which re-locks).
5. **`OnReviewRequested` / `OnPRCreated`(persist) / `applyApprovalDecision`** — guard the field write +
   `persistPRState` with `prState.mu`.
6. **`persistPRState`** — stays lock-free; contract: **caller holds `prState.mu`** (or state not yet
   published). Document this.

**Lock ordering (no-deadlock invariant): always `prState.mu` BEFORE `c.mu`; never the reverse.**
Verified: webhook writers fetch the pointer under `c.mu`, *release* `c.mu`, then take `prState.mu`.
`ProcessPR`/handlers hold `prState.mu` then take `c.mu` (via `removePR`, `isPRCircuitOpen`,
`lastProgressAt`). No site holds `c.mu` while acquiring `prState.mu`. Handlers only ever lock their own
PR's mutex (no A→B / B→A across PRs).

## Test
`controller_test.go`: drive `processAllPRs` + `OnReviewRequested` + `SetApprovalDecision` on the SAME PR
concurrently; assert `go test -race ./internal/autopilot/...` is clean and the final stage is a legal
transition. Verify with `-race` (must fail before the fix).

## Verify
- `go vet ./internal/autopilot/...` (copylocks clean) · `go build ./...`
- `go test -race ./internal/autopilot/...` green, incl. the new concurrent-driver test.

## Blocks
- **TASK-325** (scope/size merge-gate) and **B5** (merge-retry cap) both touch `controller.go` — land
  TASK-324 first.

## Out of Scope
- The already-known `*prFailureState` race (~1984) — fold in only if it shares the lock cleanly.
