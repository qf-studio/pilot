# TASK-325: Wire the scope-drift / size-floor merge-escalation gates (dead code)

**Wave:** 1 (S) · **Pilot** · **Unblocked** — TASK-324 (per-`PRState` mutex) merged in `main` (PR #3301);
build the gate on it · **Severity:** CRITICAL · **Audit ref:** TASK-322 §critical #2 (`autopilot`)

---

## Problem

`internal/autopilot/scope_guard.go` defines `ScopeDriftReason` (line 40) and `SizeFloorReason` (line 69)
as defense-in-depth rails — its header states they exist so "a runaway executor cannot land code
unsupervised even if env config drops `require_approval`" (born from OAuth cascade #2, the 512-LoC
contaminating PR #2572). They are meant to **force** human approval before auto-merge regardless of env.

But there are **zero production callers**:
```
grep -rn 'SizeFloorReason\|ScopeDriftReason' internal/ cmd/ --include=*.go \
  | grep -v scope_guard.go | grep -v _test.go   # → nothing
```
The actual merge decision in `handleCIPassed` (`controller.go:715`/~728) gates **only** on
`c.config.ResolvedEnv().RequireApproval`. So a large (>200 net LoC) or scope-drifting PR auto-merges
with no human review whenever the env doesn't set `require_approval`. The CI-fix-side guard at ~802 even
comments "belt-and-suspenders: merge-time SizeFloor (#2594) still catches it" — but that backstop is
never wired, so the comment is false and the cascade-2 attack surface is reopened.

## Approach

### Step 1 — Call the gates before merging (S, ~45 min)
In `handleCIPassed`, **before** transitioning to `StageMerging`:
- fetch PR files (existing GitHub client call used elsewhere in the controller) + the issue title,
- call `SizeFloorReason(files)` and `ScopeDriftReason(prState.PRTitle, issueTitle)`,
- if **either** returns a non-empty reason, set `prState.Stage = StageAwaitApproval` (and notify with the
  reason), **regardless** of env `RequireApproval`.
- Mutate `prState.Stage` under the per-PRState lock introduced by TASK-324.

### Step 2 — Fix the false comment (S)
- Update the ~802 "belt-and-suspenders" comment to reflect the now-wired backstop.

### Step 3 — Tests (S, ~45 min)
- A 300-LoC PR → routes to `StageAwaitApproval`, not `StageMerging`.
- A feat-PR opened from a fix-issue (scope drift) → `StageAwaitApproval`.
- A small in-scope PR with `RequireApproval=false` → still merges (no false positives).

## Files to modify
- `internal/autopilot/controller.go` (`handleCIPassed`)
- `internal/autopilot/controller_test.go`

## Test Strategy
- Unit: the three controller cases above. Reuse existing PR-files / issue-title fetch helpers; do not add
  a new GitHub call path if one already exists in the controller.

## Effort
S (~2h). One PR. **Do not start until TASK-324 has merged.**

## Out of Scope
- Tuning the size-floor / scope-drift thresholds — use the existing `scope_guard.go` definitions as-is.
