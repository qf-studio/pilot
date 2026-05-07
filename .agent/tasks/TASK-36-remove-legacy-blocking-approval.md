# TASK-36: Remove Legacy Blocking RequestApproval Path

**Status**: 🚧 In Progress
**Created**: 2026-05-06
**Assignee**: Pilot

---

## Context

**Problem**:
The async approval pipeline (TASK-29/30/31, v2.121.0–v2.128.0) shipped behind `Config.AsyncDispatch` (default `true`). The legacy blocking `Manager.RequestApproval` path was kept "for one release cycle while async_dispatch rolls out" (per controller.go:998). With v2.128.4 in production and async verified live across 3 smoke-test PRs (2026-05-06), the bake time has elapsed.

The legacy path is now dead weight. It also has architectural risks:
- Blocks the sequential `processAllPRs` tick loop for up to 24h on `<-responseCh` waiting (the original cause of `bug_telegram_approval_callback_unwired`)
- Diverges from the audited path — bug fixes to async (TASK-33 executions persistence) don't apply
- `auto_merger.go:233` still calls the blocking `RequestApproval` from `MergePR`, meaning prod-env merges may hit the legacy path even when `AsyncDispatch=true`. Worth verifying during implementation.

**Goal**:
Delete the legacy `RequestApproval` blocking path. The async path becomes the only path; `AsyncDispatch` config knob is removed.

**Success Criteria**:
- [ ] `Manager.RequestApproval` removed from `internal/approval/manager.go`
- [ ] `Manager.IsAsyncDispatch` removed (always implicitly true)
- [ ] `Config.AsyncDispatch` field removed from `internal/approval/types.go`
- [ ] `Controller.handleAwaitApprovalLegacy` removed from `internal/autopilot/controller.go`
- [ ] Legacy fallback at `controller.go:999-1000` removed (the `if !IsAsyncDispatch ... handleAwaitApprovalLegacy` branch)
- [ ] `auto_merger.go:233` migrated to async path: call `SubmitApprovalRequest` and wait for the decision via state writer / next tick (consistent with how the controller does it)
- [ ] All tests for legacy path removed; remaining tests still pass
- [ ] No references to `AsyncDispatch` in any config or yaml docs (grep clean)
- [ ] `make test`, `make lint`, `make build` all pass

---

## Implementation Plan

### Phase 1: Migrate auto_merger.go to async path
**Goal**: Eliminate the only non-controller caller of `RequestApproval`.

**Tasks**:
- [ ] Replace `m.approvalMgr.RequestApproval(ctx, req)` at `auto_merger.go:233` with `SubmitApprovalRequest` + read decision from `prState` on subsequent invocation OR refactor the merge flow so `MergePR` is called only after the controller's stage machine has set `prState.ApprovalDecision`. Likely cleaner: remove the in-line approval call from `MergePR` entirely; the controller's `handleAwaitApproval` is the single approval gate, and `MergePR` should only run for PRs that have `prState.ApprovalDecision == approved` (or env-disabled-approval).
- [ ] Update `auto_merger_test.go` cases that exercise approval inside `MergePR`.

**Files**:
- `internal/autopilot/auto_merger.go`
- `internal/autopilot/auto_merger_test.go`

### Phase 2: Remove legacy path from approval package
**Tasks**:
- [ ] Delete `Manager.RequestApproval` (`manager.go:100-...`)
- [ ] Delete `Manager.IsAsyncDispatch` (`manager.go:492-494`)
- [ ] Delete `Config.AsyncDispatch` field (`types.go:82-87`); remove default-true setting in `DefaultConfig` (line 141)
- [ ] Update package docs/comments referencing legacy path

**Files**:
- `internal/approval/manager.go`
- `internal/approval/types.go`
- `internal/approval/manager_test.go`

### Phase 3: Remove legacy path from controller
**Tasks**:
- [ ] Delete `Controller.handleAwaitApprovalLegacy` (`controller.go:1113-...`)
- [ ] Delete the legacy fallback branch at `controller.go:998-1000`
- [ ] Update `handleAwaitApproval` doc comment to drop legacy mentions

**Files**:
- `internal/autopilot/controller.go`
- `internal/autopilot/controller_test.go`

### Phase 4: Sweep & docs
**Tasks**:
- [ ] `grep -rn "async_dispatch\|AsyncDispatch\|RequestApproval\|handleAwaitApprovalLegacy" --include="*.go" --include="*.yaml" --include="*.mdx"` should return only history docs (e.g. `.agent/.context-markers/`) — no live code references.
- [ ] Update `~/.pilot/config.example.yaml` if it documents `async_dispatch` (user's local config doesn't need editing — extra keys are ignored).
- [ ] Update `docs/content/features/approval-workflows.mdx` (the page added in v2.128.4 era): remove the `async_dispatch` config knob section since the option no longer exists.

**Files**:
- `docs/content/features/approval-workflows.mdx`
- Any example yaml in repo

---

## Technical Decisions

| Decision | Options | Chosen | Reasoning |
|----------|---------|--------|-----------|
| Approval call site for prod-env merges | Keep in `auto_merger.MergePR` (sync) / move to controller stage machine (async) | Move to controller stage machine | Single approval gate; consistent with non-prod async flow; eliminates code duplication |
| Removal vs deprecation | Mark deprecated then remove next release / hard-remove now | Hard-remove now | One release of bake time per TASK-31 has elapsed; live-verified in 3 smoke-tests; deprecation adds noise without value |
| Backward compat for users with `async_dispatch: false` in config | Keep config field as no-op / hard-remove | Hard-remove (extra YAML keys are ignored by Go decoder) | Forcing migration is fine; the option was always documented as transitional |

---

## Dependencies

**Requires**:
- [x] Async path verified live (TASK-33 / v2.128.4)
- [x] One release of bake time (v2.121.0 → v2.128.4 = ~24h, exceeds the 1-release commitment)

**Blocks**: None

---

## Verify

```bash
make test
make lint
make build

# Confirm no live references remain
grep -rn "async_dispatch\|AsyncDispatch\|RequestApproval\|handleAwaitApprovalLegacy" \
  --include="*.go" --include="*.yaml" --include="*.mdx" \
  internal/ docs/ cmd/ | grep -v "\.agent/" | grep -v archive
# Expect: no matches in production code
```

After deploy: file a smoke-test approval-gated PR; verify pipeline still works exactly as before (because the async path is unchanged).

---

## Done

- [ ] All five legacy symbols removed (RequestApproval, IsAsyncDispatch, AsyncDispatch, handleAwaitApprovalLegacy, fallback branch)
- [ ] auto_merger no longer calls RequestApproval
- [ ] All tests pass
- [ ] grep sweep clean
- [ ] PR opened, CI green, approved, merged, released

---

## Notes

- Per memory `pattern_burst_auto_release_starvation.md`: ship this on its own (don't burst-merge with other PRs) to avoid auto-release skip until v2.128.4 hot-upgrade is confirmed across the controller path.
- Net LoC change estimated -150 to -250 (counting tests).
- This is the final cleanup of the trifecta arc that started with TASK-26 (deterministic handler selection) and ended with TASK-35 (non-blocking post-merge CI).

---

**Last Updated**: 2026-05-06
